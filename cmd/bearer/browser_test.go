package main

import (
	"os"
	"reflect"
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

func TestBrowserEnvironmentNeverContainsLauncherSecrets(t *testing.T) {
	input := []string{
		"PATH=/usr/bin",
		"OSANWE_SECRET=relay-secret",
		"osanwe_receipt=one-shot-entitlement",
		"DISPLAY=:0",
	}
	want := []string{"PATH=/usr/bin", "DISPLAY=:0"}
	if got := browserEnvironment(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("browser environment = %q, want %q", got, want)
	}
}

func TestLauncherEnvironmentIsConsumedBeforeHelpersStart(t *testing.T) {
	t.Setenv("OSANWE_SECRET", "relay-secret")
	t.Setenv("OSANWE_RECEIPT", "one-shot-entitlement")

	secret, receipt, err := consumeLauncherEnvironment()
	if err != nil {
		t.Fatalf("consumeLauncherEnvironment: %v", err)
	}
	if secret != "relay-secret" || receipt != "one-shot-entitlement" {
		t.Fatalf("consumed %q and %q", secret, receipt)
	}
	for _, name := range []string{"OSANWE_SECRET", "OSANWE_RECEIPT"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Fatalf("%s remains available for a child process", name)
		}
	}
}

func TestUnsupportedBrowserPlatformIsExplained(t *testing.T) {
	_, _, err := browserCommand("plan9", "http://localhost:8080/_osanwe/")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
}
