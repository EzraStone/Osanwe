package main

import (
	"strings"
	"testing"
)

func TestBrowserCommandOpensOnlyLoopback(t *testing.T) {
	for _, osName := range []string{"windows", "darwin", "linux"} {
		name, args, err := browserCommand(osName, "http://127.0.0.1:8080/_osanwe/")
		if err != nil || name == "" || len(args) == 0 {
			t.Fatalf("%s command = %q %q, %v", osName, name, args, err)
		}
	}
	for _, raw := range []string{
		"https://127.0.0.1/_osanwe/",
		"http://example.com/_osanwe/",
		"javascript:alert(1)",
	} {
		if _, _, err := browserCommand("linux", raw); err == nil {
			t.Fatalf("accepted browser URL %q", raw)
		}
	}
}

func TestUnsupportedBrowserPlatformIsExplained(t *testing.T) {
	_, _, err := browserCommand("plan9", "http://localhost:8080/_osanwe/")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
}
