package mint

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testDailyInviteBook(t *testing.T, keyID string, start time.Time) []byte {
	t.Helper()
	book := inviteBookFile{
		SchemaVersion:    dailyInviteSchemaVersion,
		ProgramID:        "wallet-test",
		MintKeyID:        keyID,
		NotBefore:        start.Format(inviteTimeFormat),
		NotAfter:         start.Add(48 * time.Hour).Format(inviteTimeFormat),
		VoucherCount:     4,
		Seed:             base64.RawURLEncoding.EncodeToString(make([]byte, inviteSeedBytes)),
		VouchersPerEpoch: 2,
		Epochs: []inviteBookEpochFile{
			{NotBefore: start.Format(inviteTimeFormat), NotAfter: start.Add(24 * time.Hour).Format(inviteTimeFormat)},
			{NotBefore: start.Add(24 * time.Hour).Format(inviteTimeFormat), NotAfter: start.Add(48 * time.Hour).Format(inviteTimeFormat)},
		},
	}
	data, err := json.Marshal(book)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testInviteWalletPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private-wallet")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "trial-wallet.db")
}

func TestInviteWalletPersistsQuotaAndTokensAcrossRestart(t *testing.T) {
	priv := key(t)
	m, err := New(priv, OpenAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(m, quietLog()).Handler())
	defer server.Close()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := start
	statePath := testInviteWalletPath(t)
	client := &Client{URL: server.URL, ExpectKeyID: m.KeyID()}

	wallet, err := OpenInviteWallet(InviteWalletConfig{
		Client: client, StatePath: statePath, Batch: 1, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.Take(context.Background()); !errors.Is(err, ErrInviteActivationRequired) {
		t.Fatalf("inactive Take = %v, want ErrInviteActivationRequired", err)
	}
	book := testDailyInviteBook(t, m.KeyID(), start)
	if err := wallet.ActivateInviteBook(book); err != nil {
		t.Fatalf("ActivateInviteBook: %v", err)
	}
	if err := wallet.ActivateInviteBook(book); err != nil {
		t.Fatalf("same-book reactivation was not idempotent: %v", err)
	}
	first, err := wallet.Take(context.Background())
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}
	if err := Verify(&priv.PublicKey, first); err != nil {
		t.Fatalf("first token: %v", err)
	}
	wallet.Put(first)
	if wallet.Len() != 1 || wallet.Spent() != 0 {
		t.Fatalf("after Put: len=%d spent=%d", wallet.Len(), wallet.Spent())
	}
	if _, err := wallet.Take(context.Background()); err != nil {
		t.Fatalf("retaking stocked token: %v", err)
	}
	if _, err := wallet.Take(context.Background()); err != nil {
		t.Fatalf("second issued token: %v", err)
	}
	if _, err := wallet.Take(context.Background()); !errors.Is(err, ErrInviteEpochExhausted) {
		t.Fatalf("third day-one token = %v, want ErrInviteEpochExhausted", err)
	}
	if err := wallet.Close(); err != nil {
		t.Fatal(err)
	}

	wallet, err = OpenInviteWallet(InviteWalletConfig{
		Client: client, StatePath: statePath, Batch: 1, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer wallet.Close()
	if got := wallet.Spent(); got != 2 {
		t.Fatalf("reopened spent = %d, want 2", got)
	}
	if _, err := wallet.Take(context.Background()); !errors.Is(err, ErrInviteEpochExhausted) {
		t.Fatalf("reopened day-one Take = %v, want ErrInviteEpochExhausted", err)
	}
	now = start.Add(24 * time.Hour)
	if _, err := wallet.Take(context.Background()); err != nil {
		t.Fatalf("day-two Take: %v", err)
	}
	status := wallet.InviteStatus()
	if !status.Activated || status.RemainingEpoch != 1 || !status.EpochEnds.Equal(start.Add(48*time.Hour)) {
		t.Fatalf("day-two status = %+v", status)
	}
}

func TestInviteWalletRejectsBookReplacementAndWrongMint(t *testing.T) {
	priv := key(t)
	m, _ := New(priv, OpenAuthorizer{})
	server := httptest.NewServer(NewServer(m, quietLog()).Handler())
	defer server.Close()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wallet, err := OpenInviteWallet(InviteWalletConfig{
		Client:    &Client{URL: server.URL, ExpectKeyID: m.KeyID()},
		StatePath: testInviteWalletPath(t), Batch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer wallet.Close()
	book := testDailyInviteBook(t, m.KeyID(), start)
	if err := wallet.ActivateInviteBook(book); err != nil {
		t.Fatal(err)
	}
	var changed map[string]any
	if err := json.Unmarshal(book, &changed); err != nil {
		t.Fatal(err)
	}
	replacementSeed := make([]byte, inviteSeedBytes)
	replacementSeed[len(replacementSeed)-1] = 1
	changed["seed"] = base64.RawURLEncoding.EncodeToString(replacementSeed)
	replacement, _ := json.Marshal(changed)
	if err := wallet.ActivateInviteBook(replacement); err == nil {
		t.Fatal("different invite book replaced active wallet")
	}
	wrong := testDailyInviteBook(t, "mint-a-different-key", start)
	if err := wallet.ActivateInviteBook(wrong); err == nil {
		t.Fatal("invite book for another mint was accepted")
	}
}

func TestInviteWalletRefusesSharedUnixDirectory(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Unix permission check")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := OpenInviteWallet(InviteWalletConfig{
		Client:    &Client{URL: "https://mint.example", ExpectKeyID: "mint-test"},
		StatePath: filepath.Join(dir, "wallet.db"),
	})
	if err == nil {
		t.Fatal("shared directory accepted for secret wallet")
	}
}

// countingHandler counts issuance requests, so a test can tell a wallet that
// stops at an exhausted day from one that keeps asking the mint.
type countingHandler struct {
	next  http.Handler
	count atomic.Int64
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		c.count.Add(1)
	}
	c.next.ServeHTTP(w, r)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInviteWalletRunKeepsAStockWithinEachDaysAllowance(t *testing.T) {
	priv := key(t)
	m, err := New(priv, OpenAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingHandler{next: NewServer(m, quietLog()).Handler()}
	server := httptest.NewServer(counter)
	defer server.Close()

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var now atomic.Value
	now.Store(start)
	wallet, err := OpenInviteWallet(InviteWalletConfig{
		Client: &Client{URL: server.URL, ExpectKeyID: m.KeyID()}, StatePath: testInviteWalletPath(t),
		Batch: 2, Now: func() time.Time { return now.Load().(time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer wallet.Close()
	if err := wallet.ActivateInviteBook(testDailyInviteBook(t, m.KeyID(), start)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wallet.Run(ctx); close(done) }()

	// Day one allows two vouchers, and the stock target is two.
	waitFor(t, "the first fill", func() bool { return wallet.Len() == 2 })
	issued := counter.count.Load()
	tok, err := wallet.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(&priv.PublicKey, tok); err != nil {
		t.Fatalf("stocked token does not verify: %v", err)
	}

	// The refill this triggers finds the day exhausted. It must stop there
	// rather than keep asking the mint.
	time.Sleep(100 * time.Millisecond)
	if got := counter.count.Load(); got != issued {
		t.Fatalf("an exhausted day caused %d more issuance requests", got-issued)
	}
	if wallet.Len() != 1 {
		t.Fatalf("stock = %d after taking one of two on an exhausted day", wallet.Len())
	}

	// On day two, taking a token triggers a refill from the new allowance.
	now.Store(start.Add(24 * time.Hour))
	if _, err := wallet.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the day-two refill", func() bool { return wallet.Len() == 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
