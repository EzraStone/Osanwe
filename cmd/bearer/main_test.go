package main

import (
	"os"
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
