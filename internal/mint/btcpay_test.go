package mint

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func btcpayTestAuthorizer(t *testing.T, handler http.HandlerFunc, receipts ReceiptStore) (*BTCPayAuthorizer, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	a, err := NewBTCPayAuthorizer(BTCPayConfig{
		Endpoint: server.URL, StoreID: "store_123", APIKey: "api_key",
		Amount: "12.50", Currency: "USD", Receipts: receipts,
	})
	if err != nil {
		t.Fatalf("NewBTCPayAuthorizer: %v", err)
	}
	return a, server
}

func settledInvoice(w http.ResponseWriter, id, status, amount, currency string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%q,"storeId":"store_123","status":%q,"amount":%q,"currency":%q}`,
		id, status, amount, currency)
}

func TestBTCPayAuthorizerConsumesOneSettledInvoice(t *testing.T) {
	var hits atomic.Int64
	a, _ := btcpayTestAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/api/v1/stores/store_123/invoices/invoice_abc" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "token api_key" {
			t.Errorf("Authorization = %q", got)
		}
		settledInvoice(w, "invoice_abc", "Settled", "12.500", "USD")
	}, NewMemoryReceiptStore())

	if err := a.Authorize(context.Background(), []byte("invoice_abc")); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := a.Authorize(context.Background(), []byte("invoice_abc")); !errors.Is(err, ErrReceiptUsed) {
		t.Fatalf("second Authorize = %v, want ErrReceiptUsed", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("BTCPay hits = %d, want 2", hits.Load())
	}
}

func TestBTCPayAuthorizerDoesNotConsumeAnUnsettledInvoice(t *testing.T) {
	var settled atomic.Bool
	a, _ := btcpayTestAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		status := "Processing"
		if settled.Load() {
			status = "Settled"
		}
		settledInvoice(w, "invoice_abc", status, "12.50", "USD")
	}, NewMemoryReceiptStore())

	if err := a.Authorize(context.Background(), []byte("invoice_abc")); err == nil {
		t.Fatal("authorized an unsettled invoice")
	}
	settled.Store(true)
	if err := a.Authorize(context.Background(), []byte("invoice_abc")); err != nil {
		t.Fatalf("settled invoice was consumed by the earlier refusal: %v", err)
	}
}

func TestBTCPayAuthorizerChecksExactPrice(t *testing.T) {
	tests := []struct {
		name, amount, currency string
	}{
		{"wrong amount", "0.01", "USD"},
		{"wrong currency", "12.50", "EUR"},
		{"malformed amount", "not-money", "USD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := btcpayTestAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
				settledInvoice(w, "invoice_abc", "Settled", tc.amount, tc.currency)
			}, NewMemoryReceiptStore())
			if err := a.Authorize(context.Background(), []byte("invoice_abc")); err == nil {
				t.Fatal("authorized an invoice with the wrong price")
			}
		})
	}
}

func TestBTCPayAuthorizerAllowsOnlyOneConcurrentClaim(t *testing.T) {
	a, _ := btcpayTestAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		settledInvoice(w, "invoice_abc", "Settled", "12.50", "USD")
	}, NewMemoryReceiptStore())

	const attempts = 20
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.Authorize(context.Background(), []byte("invoice_abc"))
		}()
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrReceiptUsed) {
			t.Fatalf("unexpected Authorize error: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted = %d, want exactly 1", accepted)
	}
}

func TestBTCPayAuthorizerNeverFollowsRedirectsWithTheAPIKey(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		settledInvoice(w, "invoice_abc", "Settled", "12.50", "USD")
	}))
	defer destination.Close()
	a, _ := btcpayTestAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}, NewMemoryReceiptStore())

	if err := a.Authorize(context.Background(), []byte("invoice_abc")); err == nil {
		t.Fatal("authorized a redirect response")
	}
	if leaked.Load() {
		t.Fatal("BTCPay API key followed a redirect")
	}
}

func TestBTCPayAuthorizerRejectsUnsafeConfiguration(t *testing.T) {
	good := BTCPayConfig{
		Endpoint: "https://payments.example", StoreID: "store_123", APIKey: "api_key",
		Amount: "12.50", Currency: "USD", Receipts: NewMemoryReceiptStore(),
	}
	tests := []struct {
		name string
		mut  func(*BTCPayConfig)
	}{
		{"plaintext remote endpoint", func(c *BTCPayConfig) { c.Endpoint = "http://payments.example" }},
		{"endpoint credential", func(c *BTCPayConfig) { c.Endpoint = "https://user:pass@payments.example" }},
		{"missing store", func(c *BTCPayConfig) { c.StoreID = "" }},
		{"unsafe API key", func(c *BTCPayConfig) { c.APIKey = "key\r\nX-Leak: yes" }},
		{"missing price", func(c *BTCPayConfig) { c.Amount = "" }},
		{"zero price", func(c *BTCPayConfig) { c.Amount = "0" }},
		{"fraction syntax", func(c *BTCPayConfig) { c.Amount = "1/2" }},
		{"whitespace-padded price", func(c *BTCPayConfig) { c.Amount = " 12.50" }},
		{"missing currency", func(c *BTCPayConfig) { c.Currency = "" }},
		{"unsafe currency", func(c *BTCPayConfig) { c.Currency = "US D" }},
		{"missing receipt store", func(c *BTCPayConfig) { c.Receipts = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.mut(&cfg)
			if _, err := NewBTCPayAuthorizer(cfg); err == nil {
				t.Fatal("accepted unsafe configuration")
			}
		})
	}
}
