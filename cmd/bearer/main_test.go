package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
