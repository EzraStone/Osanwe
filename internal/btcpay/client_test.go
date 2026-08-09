package btcpay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Config{Endpoint: server.URL, StoreID: "store_123", APIKey: "api_key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, server
}

func writeInvoice(w http.ResponseWriter, server, id, amount, currency, status string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%q,"storeId":"store_123","amount":%q,"currency":%q,"status":%q,"checkoutLink":%q}`,
		id, amount, currency, status, server+"/i/"+id)
}

func TestGetInvoiceUsesTheNarrowAuthenticatedEndpoint(t *testing.T) {
	var serverURL string
	client, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/stores/store_123/invoices/invoice_abc" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "token api_key" {
			t.Errorf("Authorization = %q", got)
		}
		writeInvoice(w, serverURL, "invoice_abc", "12.50", "USD", "Settled")
	})
	serverURL = server.URL

	invoice, err := client.GetInvoice(context.Background(), "invoice_abc")
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if invoice.ID != "invoice_abc" || invoice.Status != "Settled" || invoice.Amount != "12.50" {
		t.Fatalf("invoice = %+v", invoice)
	}
}

func TestCreateInvoiceSendsOnlyTheFixedPrice(t *testing.T) {
	var serverURL string
	client, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/stores/store_123/invoices" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body) != 2 || body["amount"] != "12.50" || body["currency"] != "USD" {
			t.Errorf("request body = %#v", body)
		}
		writeInvoice(w, serverURL, "invoice_new", "12.500", "USD", "New")
	})
	serverURL = server.URL

	invoice, err := client.CreateInvoice(context.Background(), "12.50", "usd")
	if err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if invoice.ID != "invoice_new" || invoice.CheckoutLink != server.URL+"/i/invoice_new" {
		t.Fatalf("invoice = %+v", invoice)
	}
}

func TestCreateInvoiceRejectsAChangedPriceOrCheckoutOrigin(t *testing.T) {
	tests := []struct {
		name, amount, currency, checkout string
	}{
		{"amount", "0.01", "USD", ""},
		{"currency", "12.50", "EUR", ""},
		{"origin", "12.50", "USD", "https://attacker.example/i/invoice_new"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var serverURL string
			client, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				checkout := tc.checkout
				if checkout == "" {
					checkout = serverURL + "/i/invoice_new"
				}
				fmt.Fprintf(w, `{"id":"invoice_new","storeId":"store_123","amount":%q,"currency":%q,"status":"New","checkoutLink":%q}`,
					tc.amount, tc.currency, checkout)
			})
			serverURL = server.URL
			if _, err := client.CreateInvoice(context.Background(), "12.50", "USD"); err == nil {
				t.Fatal("accepted a changed invoice response")
			}
		})
	}
}

func TestClientNeverFollowsRedirectsWithTheAPIKey(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	defer destination.Close()
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	})

	if _, err := client.GetInvoice(context.Background(), "invoice_abc"); err == nil {
		t.Fatal("accepted redirect response")
	}
	if leaked.Load() {
		t.Fatal("API key followed a redirect")
	}
}

func TestClientRejectsUnsafeConfiguration(t *testing.T) {
	good := Config{Endpoint: "https://payments.example", StoreID: "store_123", APIKey: "api_key"}
	tests := []struct {
		name string
		mut  func(*Config)
	}{
		{"remote plaintext", func(c *Config) { c.Endpoint = "http://payments.example" }},
		{"endpoint credential", func(c *Config) { c.Endpoint = "https://user:pass@payments.example" }},
		{"endpoint query", func(c *Config) { c.Endpoint = "https://payments.example?x=1" }},
		{"missing store", func(c *Config) { c.StoreID = "" }},
		{"unsafe store", func(c *Config) { c.StoreID = "../other" }},
		{"missing key", func(c *Config) { c.APIKey = "" }},
		{"header injection", func(c *Config) { c.APIKey = "key\r\nX-Leak: yes" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.mut(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("accepted unsafe configuration")
			}
		})
	}
}

func TestExactPriceHelpers(t *testing.T) {
	for _, value := range []string{"1", "1.00", ".5", "12.500"} {
		if _, ok := ExactPositiveDecimal(value); !ok {
			t.Errorf("ExactPositiveDecimal(%q) rejected", value)
		}
	}
	for _, value := range []string{"", "0", "-1", "1/2", "1e2", " 1"} {
		if _, ok := ExactPositiveDecimal(value); ok {
			t.Errorf("ExactPositiveDecimal(%q) accepted", value)
		}
	}
	if equal, err := AmountsEqual("12.50", "12.500"); err != nil || !equal {
		t.Fatalf("AmountsEqual = %v, %v", equal, err)
	}
	if got, ok := NormalizeCurrency("usd"); !ok || got != "USD" {
		t.Fatalf("NormalizeCurrency = %q, %v", got, ok)
	}
}
