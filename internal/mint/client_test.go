package mint

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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

// A one-shot receipt is marked used before it is sent, since a lost response
// cannot be retried safely. A key fetch that failed sent nothing, so it must
// leave the receipt usable.
func TestAKeyFetchFailureDoesNotUseUpTheReceipt(t *testing.T) {
	m, server, down, issues := flakyMint(t)
	wallet := NewWallet(&Client{URL: server.URL, ExpectKeyID: m.KeyID()}, "invoice_1", 1)

	if _, err := wallet.Take(context.Background()); err == nil {
		t.Fatal("bought a token while the mint's key was unavailable")
	}
	if n := issues.Load(); n != 0 {
		t.Fatalf("the receipt reached /issue %d time(s) without a key", n)
	}
	down.Store(false)
	if _, err := wallet.Take(context.Background()); err != nil {
		t.Fatalf("the receipt was used up by a request the mint never saw: %v", err)
	}
}

func TestAKeyFetchFailureDoesNotSpendAnInviteVoucher(t *testing.T) {
	m, server, down, issues := flakyMint(t)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wallet, err := OpenInviteWallet(InviteWalletConfig{
		Client: &Client{URL: server.URL, ExpectKeyID: m.KeyID()}, StatePath: testInviteWalletPath(t),
		Batch: 1, Now: func() time.Time { return start },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer wallet.Close()
	if err := wallet.ActivateInviteBook(testDailyInviteBook(t, m.KeyID(), start)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, err := wallet.Take(context.Background()); err == nil || errors.Is(err, ErrInviteEpochExhausted) {
			t.Fatalf("Take %d while the key was unavailable = %v, want the fetch failure", i, err)
		}
	}
	if got := wallet.InviteStatus().RemainingEpoch; got != 2 {
		t.Fatalf("%d of 2 vouchers left after requests the mint never saw (issue calls: %d)", got, issues.Load())
	}
	down.Store(false)
	if _, err := wallet.Take(context.Background()); err != nil {
		t.Fatalf("Take once the mint was back: %v", err)
	}
	if got := wallet.InviteStatus().RemainingEpoch; got != 1 {
		t.Fatalf("%d vouchers left after one real issuance, want 1", got)
	}
}
