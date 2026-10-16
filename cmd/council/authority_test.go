package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every refusal must come before the authority identity is created: that key
// is what clients trust, and a refused command line must not leave one behind.
func TestAuthorityRefusesBadSettingsWithoutCreatingAnIdentity(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no consensus lifetime", []string{"-lifetime", "0"}, "-lifetime must be positive"},
		{"rebuild no shorter than the lifetime", []string{"-rebuild", "30m", "-lifetime", "10m"}, "must be shorter than -lifetime"},
		{"a zero rebuild interval", []string{"-rebuild", "0"}, "epoch must be a whole number of seconds"},
		{"a fractional rebuild interval", []string{"-rebuild", "1500ms"}, "epoch must be a whole number of seconds"},
		{"dropping relays after no failures", []string{"-unhealthy-after", "0"}, "-unhealthy-after must be at least 1"},
		{"a zero probe timeout", []string{"-probe-timeout", "0"}, "-probe-timeout must be more than zero"},
		{"a negative probe timeout", []string{"-probe-timeout", "-5s"}, "-probe-timeout must be more than zero"},
		{"a probe timeout longer than a probing round", []string{"-probe-timeout", "5m"}, "at most 2m0s"},
		{"an accept list that is not there", []string{"-accept", "no-such-accept-list"}, "no-such-accept-list"},
		{"an unknown flag", []string{"-publish-anything"}, "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			identity := filepath.Join(dir, "council.key")
			args := append([]string{
				"-addr", "127.0.0.1:0", "-identity", identity, "-descriptors", filepath.Join(dir, "descriptors"),
			}, test.args...)
			done := make(chan error, 1)
			go func() { done <- runArgs(args) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("the authority started serving instead of refusing")
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want one containing %q", err, test.want)
			}
			if _, statErr := os.Stat(identity); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("a refused authority created an identity at %s (stat error %v)", identity, statErr)
			}
		})
	}
}

func TestUnknownSubcommandIsNamed(t *testing.T) {
	err := runArgs([]string{"consensus"})
	if err == nil || !strings.Contains(err.Error(), `unknown subcommand "consensus"`) {
		t.Fatalf("run error = %v", err)
	}
}
