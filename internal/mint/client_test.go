package mint

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCheckMintURLKeepsReceiptsOffPlainHTTP(t *testing.T) {
	for raw, want := range map[string]string{
		"https://mint.example":          "",
		"https://mint.example/base/":    "",
		"http://127.0.0.1:8445":         "",
		"http://[::1]:8445":             "",
		"http://localhost:8445":         "",
		"http://mint.example":           "must use HTTPS",
		"http://10.0.0.5:8445":          "must use HTTPS",
		"ftp://mint.example":            "must use HTTPS",
		"mint.example":                  "must use HTTPS",
		"":                              "must use HTTPS",
		"https://user:pw@mint.example":  "must not contain user information",
		"https://mint.example/?key=v":   "must not contain user information",
		"https://mint.example/#section": "must not contain user information",
	} {
		err := CheckMintURL(raw)
		switch {
		case want == "" && err != nil:
			t.Errorf("CheckMintURL(%q) = %v, want it accepted", raw, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("CheckMintURL(%q) = %v, want one containing %q", raw, err, want)
		}
	}
}

// A redirect from /issue must not carry the receipt anywhere else. Go's client
// re-sends a POST body on 307 and 308, to whatever host Location names.
func TestTheClientDoesNotFollowAMintRedirect(t *testing.T) {
	m, err := New(key(t), OpenAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	var leaked atomic.Value
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		leaked.Store(string(body))
		http.Error(w, "not a mint", http.StatusTeapot)
	}))
	defer elsewhere.Close()
	handler := NewServer(m, quietLog()).Handler()
	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/issue" {
			http.Redirect(w, r, elsewhere.URL+"/collect", http.StatusTemporaryRedirect)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer mintServer.Close()

	client := &Client{URL: mintServer.URL, ExpectKeyID: m.KeyID(), HTTPClient: mintServer.Client()}
	if _, err := client.Token(context.Background(), "invoice_secret"); err == nil {
		t.Fatal("a redirected issuance produced a token")
	}
	if body, _ := leaked.Load().(string); body != "" {
		t.Fatalf("the redirect was followed and the request body sent on: %s", body)
	}
}

func TestTheClientRefusesARemoteMintOverPlainHTTP(t *testing.T) {
	client := &Client{URL: "http://mint.example", ExpectKeyID: "mint-test"}
	if _, err := client.Token(context.Background(), "invoice_secret"); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("Token = %v, want the plain-HTTP refusal before any request", err)
	}
}
