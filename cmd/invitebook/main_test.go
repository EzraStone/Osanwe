package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunGeneratesBooksWithoutPrintingSecrets(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("invite generation fails closed until this platform has verified owner-only permissions")
	}
	out := filepath.Join(t.TempDir(), "beta-books")
	args := []string{
		"-program", "beta-test",
		"-mint-key-id", "mint-test-key",
		"-not-before", "2026-08-23T00:00:00Z",
		"-not-after", "2026-08-30T00:00:00Z",
		"-seats", "10",
		"-vouchers-per-invite", "10",
		"-out", out,
	}
	var stdout, stderr bytes.Buffer
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "10 invite book(s), 10 voucher(s) each") {
		t.Fatalf("safe summary missing from stdout: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "retained mapped copies weaken issuer-side unlinkability") {
		t.Fatalf("success output lacks retained-seed privacy warning: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}

	bookData, err := os.ReadFile(filepath.Join(out, "books", "invite-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var book struct {
		Seed string `json:"seed"`
	}
	if err := json.Unmarshal(bookData, &book); err != nil {
		t.Fatal(err)
	}
	if book.Seed == "" {
		t.Fatal("generated book has no secret seed")
	}
	terminal := stdout.String() + stderr.String()
	if strings.Contains(terminal, book.Seed) {
		t.Fatal("secret seed was printed to the terminal")
	}
	manifest, err := os.ReadFile(filepath.Join(out, "invite-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifest, []byte(book.Seed)) {
		t.Fatal("secret seed leaked into mint manifest")
	}

	stdout.Reset()
	if err := run(args, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second run = %v, want overwrite refusal", err)
	}
}

func TestRunRequiresEveryCapacityAndWindowFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"-program", "beta-test"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "are all required") {
		t.Fatalf("run = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failure printed success output: %q", stdout.String())
	}
}

func TestRunRejectsNonCanonicalTimesAndPositionalArguments(t *testing.T) {
	base := []string{
		"-program", "beta-test", "-mint-key-id", "mint-test-key",
		"-not-before", "2026-08-23T00:00:00Z", "-not-after", "2026-08-30T00:00:00Z",
		"-seats", "10", "-vouchers-per-invite", "10", "-out", filepath.Join(t.TempDir(), "out"),
	}
	for name, args := range map[string][]string{
		"offset":     replaceArg(base, "2026-08-23T00:00:00Z", "2026-08-22T19:00:00-05:00"),
		"fractional": replaceArg(base, "2026-08-23T00:00:00Z", "2026-08-23T00:00:00.1Z"),
		"positional": append(append([]string(nil), base...), "unexpected"),
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(args, &stdout, &stderr); err == nil {
				t.Fatal("invalid CLI input was accepted")
			}
		})
	}
}

func TestRunHelpIsSecretFree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-help"}, &stdout, &stderr); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(stderr.String(), "never prints seeds or vouchers") {
		t.Fatalf("help lacks secret-handling warning: %q", stderr.String())
	}
}

func replaceArg(args []string, old, replacement string) []string {
	result := append([]string(nil), args...)
	for i := range result {
		if result[i] == old {
			result[i] = replacement
			break
		}
	}
	return result
}
