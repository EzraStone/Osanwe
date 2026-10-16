package mint

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// flakyMint serves a working mint whose /key endpoint fails while down is set,
// which is what a client sees when it starts during a brief mint outage.
func flakyMint(t *testing.T) (*Mint, *httptest.Server, *atomic.Bool, *atomic.Int64) {
	t.Helper()
	m, err := New(key(t), OpenAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	var down atomic.Bool
	var issues atomic.Int64
	handler := NewServer(m, quietLog()).Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key" && down.Load() {
			http.Error(w, "briefly unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/issue" {
			issues.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	down.Store(true)
	return m, server, &down, &issues
}

func TestAKeyFetchFailureIsNotRemembered(t *testing.T) {
	m, server, down, _ := flakyMint(t)
	client := &Client{URL: server.URL, ExpectKeyID: m.KeyID()}

	if _, err := client.Token(context.Background(), ""); err == nil {
		t.Fatal("bought a token while the mint's key was unavailable")
	}
	down.Store(false)
	if _, err := client.Token(context.Background(), ""); err != nil {
		t.Fatalf("the mint is back, but the client still fails: %v", err)
	}
}
