package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/EzraStone/osanwe/internal/btcpay"
)

type fakeCreator struct {
	mu       sync.Mutex
	calls    int
	amount   string
	currency string
	err      error
}

func (f *fakeCreator) CreateInvoice(_ context.Context, amount, currency string) (*btcpay.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.amount, f.currency = amount, currency
	if f.err != nil {
		return nil, f.err
	}
	return &btcpay.Invoice{ID: "invoice_123", CheckoutLink: "https://pay.example/i/invoice_123"}, nil
}

func newTestServer(t *testing.T, creator *fakeCreator, maximum int) http.Handler {
	t.Helper()
	server, err := NewServer(Config{Creator: creator, Amount: "1.50", Currency: "usd", MaxInvoicesPerMinute: maximum})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server.Handler()
}

func TestPageContainsFixedPriceWithoutThirdPartyResources(t *testing.T) {
	handler := newTestServer(t, &fakeCreator{}, 3)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `data-amount="1.50" data-currency="USD"`) {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "https://") {
		t.Fatal("checkout page contains a third-party resource")
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	if got := recorder.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("Set-Cookie = %q", got)
	}
}

func TestCreateUsesServerSidePriceAndReturnsReceipt(t *testing.T) {
	creator := &fakeCreator{}
	handler := newTestServer(t, creator, 3)
	request := httptest.NewRequest(http.MethodPost, "/api/checkout", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	creator.mu.Lock()
	if creator.calls != 1 || creator.amount != "1.50" || creator.currency != "USD" {
		t.Errorf("creator = calls %d, %s %s", creator.calls, creator.amount, creator.currency)
	}
	creator.mu.Unlock()
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["invoice_id"] != "invoice_123" || body["checkout_url"] != "https://pay.example/i/invoice_123" {
		t.Fatalf("response = %#v", body)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestCreateRejectsBuyerFieldsAndCrossOriginRequests(t *testing.T) {
	tests := []struct {
		name, body, contentType, fetchSite string
		want                               int
	}{
		{"buyer fields", `{"email":"buyer@example.com"}`, "application/json", "same-origin", http.StatusBadRequest},
		{"wrong media type", `{}`, "text/plain", "same-origin", http.StatusUnsupportedMediaType},
		{"cross site", `{}`, "application/json", "cross-site", http.StatusForbidden},
		{"trailing value", `{} {}`, "application/json", "same-origin", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			creator := &fakeCreator{}
			handler := newTestServer(t, creator, 3)
			request := httptest.NewRequest(http.MethodPost, "/api/checkout", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			request.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
			if creator.calls != 0 {
				t.Fatal("invalid request created an invoice")
			}
		})
	}
}

func TestCheckoutRejectsUnknownPathsQueriesAndForeignOrigins(t *testing.T) {
	handler := newTestServer(t, &fakeCreator{}, 3)
	tests := []struct {
		method, target, origin string
		want                   int
	}{
		{http.MethodGet, "/missing", "", http.StatusNotFound},
		{http.MethodGet, "/?tracking=value", "", http.StatusBadRequest},
		{http.MethodPost, "/api/checkout?price=0", "", http.StatusBadRequest},
		{http.MethodPost, "/api/checkout", "https://other.example", http.StatusForbidden},
	}
	for _, tc := range tests {
		request := httptest.NewRequest(tc.method, tc.target, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", tc.origin)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != tc.want {
			t.Errorf("%s %s with origin %q = %d, want %d", tc.method, tc.target, tc.origin, recorder.Code, tc.want)
		}
	}
}

func TestCheckoutAcceptsItsOwnOrigin(t *testing.T) {
	handler := newTestServer(t, &fakeCreator{}, 3)
	request := httptest.NewRequest(http.MethodPost, "https://checkout.example/api/checkout", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://checkout.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("same-origin status = %d, body %q", recorder.Code, recorder.Body.String())
	}
}

func TestCreateHasAnIdentityFreeGlobalLimit(t *testing.T) {
	creator := &fakeCreator{}
	handler := newTestServer(t, creator, 1)
	for attempt, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodPost, "/api/checkout", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, recorder.Code, want)
		}
	}
	if creator.calls != 1 {
		t.Fatalf("creator calls = %d", creator.calls)
	}
}

func TestCreateDoesNotLeakProviderErrors(t *testing.T) {
	handler := newTestServer(t, &fakeCreator{err: errors.New("secret upstream detail")}, 3)
	request := httptest.NewRequest(http.MethodPost, "/api/checkout", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response, _ := io.ReadAll(recorder.Result().Body)
	if recorder.Code != http.StatusBadGateway || strings.Contains(string(response), "secret") {
		t.Fatalf("response = %d %q", recorder.Code, response)
	}
}

func TestNewServerRejectsUnsafeConfiguration(t *testing.T) {
	creator := &fakeCreator{}
	for _, cfg := range []Config{
		{Amount: "1", Currency: "USD", MaxInvoicesPerMinute: 1},
		{Creator: creator, Amount: "free", Currency: "USD", MaxInvoicesPerMinute: 1},
		{Creator: creator, Amount: "1", Currency: "$", MaxInvoicesPerMinute: 1},
		{Creator: creator, Amount: "1", Currency: "USD", MaxInvoicesPerMinute: 0},
	} {
		if _, err := NewServer(cfg); err == nil {
			t.Fatalf("accepted config: %+v", cfg)
		}
	}
}
