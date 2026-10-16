package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/mint"
)

func TestCertNamesTrimsAndDefaults(t *testing.T) {
	tests := []struct {
		hosts string
		want  []string
	}{
		{"", []string{"localhost", "127.0.0.1"}},
		{" , ,", []string{"localhost", "127.0.0.1"}},
		{"gw.example", []string{"gw.example"}},
		{"gw.example, 10.0.0.5", []string{"gw.example", "10.0.0.5"}},
		{" gw.example ,,10.0.0.5 ", []string{"gw.example", "10.0.0.5"}},
	}
	for _, test := range tests {
		if got := certNames(test.hosts); !reflect.DeepEqual(got, test.want) {
			t.Errorf("certNames(%q) = %q, want %q", test.hosts, got, test.want)
		}
	}
}

// The point of trimming: an address written after a comma and a space must
// end up as an IP entry a client can verify against, not as a DNS name.
func TestSpacedHostsYieldAnIPAddressEntry(t *testing.T) {
	dir := t.TempDir()
	cert, _, _, err := certs.LoadOrCreate(filepath.Join(dir, "gw.crt"), filepath.Join(dir, "gw.key"),
		certNames("gw.example, 10.0.0.5"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.Leaf.VerifyHostname("10.0.0.5"); err != nil {
		t.Fatalf("certificate does not cover 10.0.0.5: %v (DNS %q, IP %v)", err, cert.Leaf.DNSNames, cert.Leaf.IPAddresses)
	}
	for _, name := range cert.Leaf.DNSNames {
		if name != "gw.example" {
			t.Errorf("unexpected DNS name %q", name)
		}
	}
}

var (
	mintKeyOnce sync.Once
	mintKeyPath string
	mintKeyErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if mintKeyPath != "" {
		os.RemoveAll(filepath.Dir(mintKeyPath))
	}
	os.Exit(code)
}

// testMintKey writes one mint public key for the whole package; generating an
// RSA key per case would dominate the run time for no extra coverage.
func testMintKey(t *testing.T) string {
	t.Helper()
	mintKeyOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mithlond-test-mint")
		if err != nil {
			mintKeyErr = err
			return
		}
		priv, err := mint.GenerateKey(mint.MinKeyBits)
		if err != nil {
			mintKeyErr = err
			return
		}
		mintKeyPath = filepath.Join(dir, "mint.pub")
		mintKeyErr = mint.WritePublicKey(&priv.PublicKey, mintKeyPath)
	})
	if mintKeyErr != nil {
		t.Fatal(mintKeyErr)
	}
	return mintKeyPath
}

func TestRunRefusesAnUnboundedOrUnauthenticatedGateway(t *testing.T) {
	mintKey := testMintKey(t)
	emptyCA := filepath.Join(t.TempDir(), "empty-ca.pem")
	if err := os.WriteFile(emptyCA, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	providerKey := func(name string) string {
		if name == "OSANWE_PROVIDER_KEY" {
			return "sk-test"
		}
		return ""
	}
	noKey := func(string) string { return "" }
	paddedKey := func(name string) string {
		if name == "OSANWE_PROVIDER_KEY" {
			return "sk-test\n"
		}
		return ""
	}

	tests := []struct {
		name   string
		args   []string
		getenv func(string) string
		want   string
	}{
		{"no spent-token journal", []string{"-spent-db", "", "-mint-key", mintKey}, providerKey, "-spent-db is required"},
		{"blank spent-token journal", []string{"-spent-db", "  ", "-mint-key", mintKey}, providerKey, "-spent-db is required"},
		{"no aggregate budget", []string{"-budget-db", "", "-mint-key", mintKey}, providerKey, "-budget-db is required"},
		{"unparseable cost ceiling", []string{"-budget-cost-usd", "lots", "-mint-key", mintKey}, providerKey, "invalid -budget-cost-usd"},
		{"single prices beside a route table", []string{"-routes", "routes.conf", "-input-usd-per-million", "1", "-output-usd-per-million", "2"}, providerKey, "cannot be used with -routes"},
		{"half a price pair", []string{"-input-usd-per-million", "1", "-mint-key", mintKey}, providerKey, "must be set together"},
		{"unparseable input price", []string{"-input-usd-per-million", "one", "-output-usd-per-million", "2", "-mint-key", mintKey}, providerKey, "invalid -input-usd-per-million"},
		{"no provider credential", []string{"-mint-key", mintKey}, noKey, "no provider credential"},
		{"missing route table", []string{"-routes", "does-not-exist.conf", "-mint-key", mintKey}, noKey, "opening -routes"},
		{"no mint key", nil, providerKey, "no -mint-key given"},
		{"the same mint key twice", []string{"-mint-key", mintKey, "-mint-key", mintKey}, providerKey, "holds a key already loaded"},
		{"upstream CA with no certificates", []string{"-mint-key", mintKey, "-upstream-ca", emptyCA}, providerKey, "contained no usable certificates"},
		{"zero request ceiling", []string{"-mint-key", mintKey, "-budget-requests", "0"}, providerKey, "-budget-requests cannot be 0"},
		{"zero input ceiling", []string{"-mint-key", mintKey, "-budget-input-bytes", "0"}, providerKey, "-budget-input-bytes cannot be 0"},
		{"zero output ceiling", []string{"-mint-key", mintKey, "-budget-output-tokens", "0"}, providerKey, "-budget-output-tokens cannot be 0"},
		{"zero budget window", []string{"-mint-key", mintKey, "-budget-window", "0"}, providerKey, "-budget-window cannot be 0"},
		{"zero request size", []string{"-mint-key", mintKey, "-max-request-bytes", "0"}, providerKey, "-max-request-bytes cannot be 0"},
		{"zero output limit", []string{"-mint-key", mintKey, "-max-output-tokens", "0"}, providerKey, "-max-output-tokens cannot be 0"},
		{"zero burst ceiling", []string{"-mint-key", mintKey, "-burst-budget-db", "burst.db", "-burst-budget-requests", "0"}, providerKey, "-burst-budget-requests cannot be 0"},
		// The table passes -cert and -key on every run, which is exactly the
		// contradiction: a certificate was named, and plaintext would ignore it.
		{"plaintext beside a certificate", []string{"-mint-key", mintKey, "-insecure-plaintext"}, providerKey, "-insecure-plaintext serves without TLS, so -cert, -key would be ignored"},
		{"burst limit without a burst database", []string{"-mint-key", mintKey, "-burst-budget-requests", "2"}, providerKey, "-burst-budget-requests has no effect without -burst-budget-db"},
		{"burst window without a burst database", []string{"-mint-key", mintKey, "-burst-budget-window", "2m", "-burst-budget-output-tokens", "100"}, providerKey, "-burst-budget-output-tokens, -burst-budget-window have no effect"},
		// The gateway and budget packages refuse these themselves; the gateway
		// must still do so before it writes a TLS key or opens a database.
		{"plain-HTTP upstream", []string{"-mint-key", mintKey, "-upstream", "http://provider.example"}, providerKey, "upstream must be https"},
		{"no models", []string{"-mint-key", mintKey, "-models", " , "}, providerKey, "Models is required without Routes"},
		{"request ceiling over the maximum", []string{"-mint-key", mintKey, "-max-request-bytes", "999999999999"}, providerKey, "MaxRequestBody must be between"},
		{"output limit over the maximum", []string{"-mint-key", mintKey, "-max-output-tokens", "1000000"}, providerKey, "MaxOutputTokens must be between"},
		{"provider key with a trailing newline", []string{"-mint-key", mintKey}, paddedKey, "surrounding whitespace"},
		{"sub-minute budget window", []string{"-mint-key", mintKey, "-budget-window", "30s"}, providerKey, "budget window must be a whole number of seconds"},
		{"sub-minute burst window", []string{"-mint-key", mintKey, "-burst-budget-db", "burst.db", "-burst-budget-window", "30s"}, providerKey, "burst budget: gateway: budget window"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			// Relative paths in a case, such as burst.db, land here too.
			t.Chdir(dir)
			args := []string{
				"-addr", "127.0.0.1:0",
				"-cert", filepath.Join(dir, "gw.crt"), "-key", filepath.Join(dir, "gw.key"),
				"-spent-db", filepath.Join(dir, "spent.db"), "-budget-db", filepath.Join(dir, "budget.db"),
				"-models", "test-model",
			}
			args = append(args, test.args...)
			done := make(chan error, 1)
			go func() { done <- run(args, test.getenv) }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("run error = %v, want one containing %q", err, test.want)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("mithlond started serving instead of refusing")
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Fatalf("refusal left %v behind (ReadDir error %v)", names, err)
			}
		})
	}
}

func TestRunTreatsHelpAsSuccess(t *testing.T) {
	if err := run([]string{"-h"}, func(string) string { return "" }); err != nil {
		t.Fatalf("-h returned %v, want nil", err)
	}
}
