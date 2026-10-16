package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

func TestSelectAuthorizationMode(t *testing.T) {
	tests := []struct {
		name                string
		open                bool
		btcpay, invite      string
		want                authorizationMode
		wantMutualExclusion bool
	}{
		{name: "none", want: authorizationModeNone},
		{name: "open", open: true, want: authorizationModeOpen},
		{name: "BTCPay", btcpay: "https://pay.example", want: authorizationModeBTCPay},
		{name: "invite", invite: "invite-manifest.json", want: authorizationModeInvite},
		{name: "open and BTCPay", open: true, btcpay: "https://pay.example", wantMutualExclusion: true},
		{name: "open and invite", open: true, invite: "invite-manifest.json", wantMutualExclusion: true},
		{name: "BTCPay and invite", btcpay: "https://pay.example", invite: "invite-manifest.json", wantMutualExclusion: true},
		{name: "all", open: true, btcpay: "https://pay.example", invite: "invite-manifest.json", wantMutualExclusion: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectAuthorizationMode(test.open, test.btcpay, test.invite)
			if test.wantMutualExclusion {
				if err == nil {
					t.Fatalf("mode = %v, want mutual-exclusion error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectAuthorizationMode: %v", err)
			}
			if got != test.want {
				t.Fatalf("mode = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateInviteCapacityFlag(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     authorizationMode
		capacity int
		wantErr  bool
	}{
		{name: "invite pinned", mode: authorizationModeInvite, capacity: 100},
		{name: "invite missing", mode: authorizationModeInvite, wantErr: true},
		{name: "invite negative", mode: authorizationModeInvite, capacity: -1, wantErr: true},
		{name: "BTCPay unchanged", mode: authorizationModeBTCPay},
		{name: "open unchanged", mode: authorizationModeOpen},
		{name: "capacity without manifest", mode: authorizationModeBTCPay, capacity: 100, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateInviteCapacityFlag(test.mode, test.capacity)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateLoadedInviteCapacity(t *testing.T) {
	if err := validateLoadedInviteCapacity(100, 100); err != nil {
		t.Fatalf("matching capacity: %v", err)
	}
	for _, actual := range []int{99, 101, 100_000} {
		if err := validateLoadedInviteCapacity(100, actual); err == nil {
			t.Fatalf("actual capacity %d was accepted against pin 100", actual)
		}
	}
}

// runRefusal runs eregion with args and returns its error, failing the test if
// it starts serving instead: every case here must be refused before listen.
func runRefusal(t *testing.T, args []string, getenv func(string) string) error {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "mint.key")
	t.Cleanup(func() {
		// A configuration that will not start has no business creating a
		// signing key on its way to saying so.
		if _, err := os.Stat(keyPath); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("refused configuration %q left a signing key at %s (stat error %v)", args, keyPath, err)
		}
	})
	full := append([]string{"-key", keyPath, "-addr", "127.0.0.1:0"}, args...)
	done := make(chan error, 1)
	go func() { done <- run(full, getenv, io.Discard) }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("run(%q) started serving instead of refusing", args)
		return nil
	}
}

func TestRunRefusesUnsafeAuthorizationConfigurations(t *testing.T) {
	noEnv := func(string) string { return "" }
	btcpayKey := func(name string) string {
		if name == "OSANWE_BTCPAY_API_KEY" {
			return "test-api-key"
		}
		return ""
	}
	tests := []struct {
		name   string
		args   []string
		getenv func(string) string
		want   string
	}{
		{"no authorization mode", nil, noEnv, "no authorization mode is configured"},
		{"BTCPay without API key", []string{"-btcpay", "https://pay.example"}, noEnv, "OSANWE_BTCPAY_API_KEY is required"},
		{"BTCPay without receipts database", []string{"-btcpay", "https://pay.example"}, btcpayKey, "-receipts-db is required in BTCPay mode"},
		{"invite without capacity", []string{"-invite-manifest", "invite-manifest.json"}, noEnv, "-invite-capacity is required"},
		{"invite without receipts database", []string{"-invite-manifest", "invite-manifest.json", "-invite-capacity", "10"}, noEnv, "-receipts-db is required in invite mode"},
		{"open and invite together", []string{"-open", "-invite-manifest", "invite-manifest.json"}, noEnv, "mutually exclusive"},
		{"capacity outside invite mode", []string{"-open", "-invite-capacity", "5"}, noEnv, "valid only with -invite-manifest"},
		{"unknown flag", []string{"-pay-anyone"}, noEnv, "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runRefusal(t, test.args, test.getenv)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

// The BTCPay client checks these itself, but only once it is built, which
// used to be after the signing key and the receipts database were created.
func TestRunRefusesBadBTCPaySettingsBeforeCreatingFiles(t *testing.T) {
	btcpayKey := func(name string) string {
		if name == "OSANWE_BTCPAY_API_KEY" {
			return "test-api-key"
		}
		return ""
	}
	for _, test := range []struct {
		name, endpoint, store, amount, currency string
		want                                    string
	}{
		{"plain HTTP", "http://pay.example", "store1", "1.00", "USD", "endpoint must use HTTPS"},
		{"endpoint with a query", "https://pay.example/?k=v", "store1", "1.00", "USD", "must not contain user information, a query"},
		{"no store", "https://pay.example", "", "1.00", "USD", "store ID is required"},
		{"no price", "https://pay.example", "store1", "", "USD", "amount must be a positive exact decimal"},
		{"zero price", "https://pay.example", "store1", "0.00", "USD", "amount must be a positive exact decimal"},
		{"no currency", "https://pay.example", "store1", "1.00", "", "currency must contain 2-12"},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipts := filepath.Join(t.TempDir(), "receipts.db")
			err := runRefusal(t, []string{
				"-btcpay", test.endpoint, "-btcpay-store", test.store,
				"-btcpay-amount", test.amount, "-btcpay-currency", test.currency,
				"-receipts-db", receipts,
			}, btcpayKey)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want one containing %q", err, test.want)
			}
			if _, err := os.Stat(receipts); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the refusal left a receipts database behind (stat error %v)", err)
			}
		})
	}
}

func TestRunPrintsTheKeyIDOfTheKeyItLoads(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "mint.key")
	var out bytes.Buffer
	if err := run([]string{"-key", keyPath, "-print-key-id"}, func(string) string { return "" }, &out); err != nil {
		t.Fatal(err)
	}
	priv, created, err := mint.LoadOrCreateKey(keyPath, mint.MinKeyBits)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("-print-key-id did not leave the key it reported on disk")
	}
	if got, want := strings.TrimSpace(out.String()), mint.KeyID(&priv.PublicKey); got != want {
		t.Fatalf("printed key id %q, want %q", got, want)
	}
}

func TestRunTreatsHelpAsSuccess(t *testing.T) {
	if err := run([]string{"-h"}, func(string) string { return "" }, io.Discard); err != nil {
		t.Fatalf("-h returned %v, want nil", err)
	}
}

// The mint must not announce that it is listening on a port it then fails to
// bind. The log line used to come first and the bind afterwards, inside
// ListenAndServe's goroutine.
func TestRunDoesNotClaimToListenOnAPortItCannotBind(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = stderr })
	logged := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		logged <- string(out)
	}()

	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-key", filepath.Join(t.TempDir(), "mint.key"), "-addr", taken.Addr().String(), "-open"},
			func(string) string { return "" }, io.Discard)
	}()
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("eregion served on a port that was already taken")
	}
	w.Close()
	os.Stderr = stderr
	out := <-logged

	if strings.Contains(out, "eregion listening") {
		t.Fatalf("logged that it was listening, then failed to bind (%v):\n%s", err, out)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "eregion: listen ") {
		t.Fatalf("run error = %v, want the bind failure", err)
	}
}
