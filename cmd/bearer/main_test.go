package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
