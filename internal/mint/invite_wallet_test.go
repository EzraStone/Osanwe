package mint

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
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
	statePath := filepath.Join(t.TempDir(), "trial-wallet.db")
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
		StatePath: filepath.Join(t.TempDir(), "wallet.db"), Batch: 1,
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
