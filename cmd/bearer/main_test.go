package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
)

func TestTokenModeCLIRequiresExplicitGatewayUpstream(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })

	for _, args := range [][]string{
		{"bearer", "-mint", "https://mint.example", "-mint-key-id", "test-key"},
		{"bearer", "-mint", "https://mint.example", "-mint-key-id", "test-key", "-upstream", "   "},
	} {
		os.Args = args
		err := run()
		if err == nil || !strings.Contains(err.Error(), "explicit gateway -upstream") {
			t.Errorf("run(%q) error = %v, want explicit gateway refusal", args, err)
		}
	}
}

func TestConfigIsAppliedBeforeConnectionValidation(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	t.Setenv("OSANWE_SECRET", "")

	path := filepath.Join(t.TempDir(), "osanwe.json")
	if err := os.WriteFile(path, []byte(`{
  "schema_version": 1,
  "relay": "relay.example:8443",
  "pin": "sha256/dGVzdA==",
  "upstream": "https://gateway.example",
  "mint": "https://mint.example",
  "mint_key_id": "mint-test"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"bearer", "-config", path}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "no secret set") {
		t.Fatalf("run error = %v, want validation to reach the runtime-only secret", err)
	}
}

func TestStdinCloseSignalWaitsForLauncherEOF(t *testing.T) {
	reader, writer := io.Pipe()
	closed := stdinCloseSignal(reader)
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	select {
	case <-closed:
		t.Fatal("stdin close signal fired while the launcher still held the pipe")
	default:
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("stdin close signal did not fire after launcher EOF")
	}
}

func TestReadInviteBookBoundsWhatItReads(t *testing.T) {
	dir := t.TempDir()
	const limit = 64 << 10
	write := func(name string, size int) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, bytes.Repeat([]byte("{"), size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	if data, err := readInviteBook(write("at-limit.json", limit)); err != nil || len(data) != limit {
		t.Fatalf("a book of exactly %d bytes: %d bytes, %v", limit, len(data), err)
	}
	if _, err := readInviteBook(write("over-limit.json", limit+1)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a book one byte over the limit: %v, want a size refusal", err)
	}
	if _, err := readInviteBook(filepath.Join(dir, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing book: %v, want not-exist", err)
	}
}

func TestStringListTrimsAndDropsEmptyItems(t *testing.T) {
	var list stringList
	for _, v := range []string{" https://a.example/consensus ,, https://b.example/consensus", "", "https://c.example/consensus "} {
		if err := list.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"https://a.example/consensus", "https://b.example/consensus", "https://c.example/consensus"}
	if !reflect.DeepEqual([]string(list), want) {
		t.Fatalf("list = %q, want %q", list, want)
	}
	if got := list.String(); got != strings.Join(want, ",") {
		t.Fatalf("String() = %q", got)
	}
}

// runBearer runs the client with args and the given environment, failing the
// test if it starts serving: every case that uses it must refuse first.
func runBearer(t *testing.T, env map[string]string, args ...string) error {
	t.Helper()
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	for _, name := range []string{"OSANWE_SECRET", "OSANWE_RECEIPT"} {
		t.Setenv(name, env[name])
	}
	os.Args = append([]string{"bearer", "-addr", "127.0.0.1:0", "-no-ui"}, args...)
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("bearer %q started serving instead of refusing", args)
		return nil
	}
}

func TestRunRefusesEveryUnsafeOrContradictoryClientConfiguration(t *testing.T) {
	garbageCA := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(garbageCA, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := map[string]string{"OSANWE_SECRET": "a-relay-secret-long-enough"}
	relay := []string{"-relay", "relay.example:8443", "-pin", "sha256/" + strings.Repeat("A", 43) + "="}
	mint := []string{"-mint", "https://mint.example", "-mint-key-id", "mint-test", "-upstream", "https://gateway.example"}
	dir := []string{"-directory", "https://directory.invalid/consensus"}
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}

	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"window flags contradict", secret, join(relay, []string{"-open-ui"}), "-open-ui cannot be combined with -no-ui"},
		{"trial access without a mint", secret, join(relay, []string{"-trial-access"}), "-trial-access needs -mint and -mint-key-id"},
		{"invitation without trial access", secret, join(relay, []string{"-invite-book", "book.json"}), "-invite-book requires -trial-access"},
		{"trial access and a receipt", secret, join(relay, mint, []string{"-trial-access", "-receipt", "invoice_1"}), "cannot be combined with a payment receipt"},
		{"buying a token without a mint", secret, []string{"-buy-token"}, "-buy-token needs -mint and -mint-key-id"},
		{"relay and directory together", secret, join(relay, dir), "use either -relay/-pin or -directory, not both"},
		{"no relay at all", secret, nil, "no -relay given"},
		{"a relay without its pin", secret, []string{"-relay", "relay.example:8443"}, "no -pin given"},
		{"no relay secret", nil, relay, "no secret set"},
		{"a relay secret with a carriage return", map[string]string{"OSANWE_SECRET": "a-relay-secret-long-enough\r"}, relay, "no client could ever send it"},
		{"a directory without authority keys", secret, dir, "-directory needs at least one -authority key"},
		{"a threshold no consensus can meet", secret, join(dir, []string{"-authority", "ed25519:a", "-authority", "ed25519:b", "-threshold", "3"}), "exceeds the 2 authority keys"},
		{"a zero threshold", secret, join(dir, []string{"-authority", "ed25519:a", "-threshold", "0"}), "-threshold must be at least 1"},
		{"a negative threshold", secret, join(dir, []string{"-authority", "ed25519:a", "-threshold", "-1"}), "-threshold must be at least 1"},
		{"a mint key id with no mint", secret, join(relay, []string{"-mint-key-id", "mint-test"}), "your own API key would still be used"},
		{"an upstream CA with no certificates", secret, join(relay, []string{"-upstream", "https://gateway.example", "-upstream-ca", garbageCA}), "contained no usable certificates"},
		{"a remote mint over plain HTTP", secret, join(relay, []string{"-mint", "http://mint.example", "-mint-key-id", "mint-test", "-upstream", "https://gateway.example"}), "must use HTTPS"},
		{"buying a token from a remote mint over plain HTTP", secret, []string{"-buy-token", "-mint", "http://mint.example", "-mint-key-id", "mint-test"}, "must use HTTPS"},
		{"a mint URL with a query", secret, join(relay, []string{"-mint", "https://mint.example/?k=v", "-mint-key-id", "mint-test", "-upstream", "https://gateway.example"}), "must not contain user information"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runBearer(t, test.env, test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

// A configuration that will be refused must be refused before the client
// does anything observable: fetching a consensus tells the directory this
// address is starting a client, and opening the trial wallet creates its
// state file. These were all refused only after one or the other.
func TestRunRefusesBeforeContactingTheDirectoryOrOpeningTheWallet(t *testing.T) {
	var fetches atomic.Int64
	directoryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		http.NotFound(w, r)
	}))
	defer directoryServer.Close()
	authority, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	garbageCA := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(garbageCA, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := map[string]string{"OSANWE_SECRET": "a-relay-secret-long-enough"}
	dir := []string{"-directory", directoryServer.URL, "-authority", authority.Fingerprint(), "-threshold", "1"}
	gateway := []string{"-upstream", "https://gateway.example"}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"a mint with no key id", append([]string{"-mint", "https://mint.example"}, gateway...), "-mint needs -mint-key-id"},
		{"a mint key id with no mint", []string{"-mint-key-id", "mint-test"}, "your own API key would still be used"},
		{"an upstream CA with no certificates", append([]string{"-upstream-ca", garbageCA}, gateway...), "contained no usable certificates"},
		{"an API style the client cannot speak", []string{"-api-style", "smoke-signals"}, "APIStyle must be"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := fetches.Load()
			err := runBearer(t, secret, append(append([]string(nil), dir...), test.args...)...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want one containing %q", err, test.want)
			}
			if n := fetches.Load() - before; n != 0 {
				t.Fatalf("the directory was fetched %d time(s) for a configuration that was then refused", n)
			}
		})
	}

	// The control: with nothing wrong, the same flags do reach the directory,
	// so the zero counts above mean the refusals came first.
	t.Run("a good configuration does fetch", func(t *testing.T) {
		before := fetches.Load()
		err := runBearer(t, secret, dir...)
		if err == nil {
			t.Fatal("run succeeded against a directory that serves nothing")
		}
		if fetches.Load() == before {
			t.Fatalf("the directory was never fetched (run error %v); the cases above prove nothing", err)
		}
	})

	t.Run("a trial wallet beside a bad API style", func(t *testing.T) {
		walletDir := t.TempDir()
		if err := os.Chmod(walletDir, 0o700); err != nil {
			t.Fatal(err)
		}
		state := filepath.Join(walletDir, "trial-wallet.db")
		err := runBearer(t, secret, "-relay", "relay.example:8443", "-pin", "sha256/"+strings.Repeat("A", 43)+"=",
			"-trial-access", "-mint", "https://mint.example", "-mint-key-id", "mint-test",
			"-upstream", "https://gateway.example", "-wallet-state", state, "-api-style", "smoke-signals")
		if err == nil || !strings.Contains(err.Error(), "APIStyle must be") {
			t.Fatalf("run error = %v, want the API style refusal", err)
		}
		if _, err := os.Stat(state); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the refusal still created the wallet state file (stat error %v)", err)
		}
	})
}
