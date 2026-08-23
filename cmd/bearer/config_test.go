package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientConfigContainsOnlyNonSecretConnectionFacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "osanwe.json")
	if err := os.WriteFile(path, []byte(`{
  "schema_version": 1,
  "relay": "relay.example:8443",
  "pin": "sha256/example",
  "upstream": "https://gateway.example",
  "upstream_ca": "gateway.crt",
  "mint": "https://mint.example",
  "mint_key_id": "mint-example"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadClientFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Relay != "relay.example:8443" || cfg.Pin != "sha256/example" || cfg.MintKeyID != "mint-example" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.UpstreamCA != filepath.Join(dir, "gateway.crt") {
		t.Fatalf("relative CA = %q", cfg.UpstreamCA)
	}
}

func TestClientConfigRejectsSecretShapedUnknownFields(t *testing.T) {
	for _, field := range []string{"secret", "receipt", "api_key", "token"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "osanwe.json")
			body := `{"schema_version":1,"` + field + `":"must-not-live-here"}`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadClientFileConfig(path)
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("error = %v, want unknown-field refusal", err)
			}
		})
	}
}

func TestClientConfigRejectsWrongSchemaAndTrailingData(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"schema_version":2}`, "schema_version"},
		{`{"schema_version":1} {}`, "more than one"},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "osanwe.json")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := loadClientFileConfig(path)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("error = %v, want %q", err, tc.want)
		}
	}
}
