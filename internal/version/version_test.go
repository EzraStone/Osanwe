package version

import (
	"strings"
	"testing"
)

func TestStringCarriesReleaseIdentity(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldVersion, oldCommit, oldDate })
	Version, Commit, Date = "v0.1.0", "abc123", "2026-08-22"
	got := String("bearer")
	for _, want := range []string{"bearer", "v0.1.0", "abc123", "2026-08-22"} {
		if !strings.Contains(got, want) {
			t.Fatalf("String = %q, missing %q", got, want)
		}
	}
}
