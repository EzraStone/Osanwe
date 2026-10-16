package policy

import (
	"strings"
	"testing"
)

// The relay matches client-supplied CONNECT targets against its allowlist.
// Whatever an operator writes and whatever a client asks for, nothing may
// panic, an accepted entry must allow exactly its own destination, and a
// target must not be allowed under one spelling and refused under another
// that names the same host.
func FuzzAllowlistAgreesWithItself(f *testing.F) {
	for _, seed := range [][2]string{
		{"api.anthropic.com", "api.anthropic.com:443"},
		{"API.Anthropic.com.:8443", "api.anthropic.com:8443"},
		{"[::1]:8080", "[::1]:8080"},
		{"127.0.0.1", "127.0.0.1:443"},
		{"api.anthropic.com", "api.anthropic.com.evil.example:443"},
		{"internal_gw.corp", "INTERNAL_GW.CORP.:443"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, entry, target string) {
		allow, err := Parse([]string{entry})
		if err != nil {
			_ = (*Allowlist)(nil).Allows(target)
			return
		}
		dest, err := ParseDestination(entry)
		if err != nil {
			t.Fatalf("Parse accepted %q but ParseDestination refused it: %v", entry, err)
		}
		if !allow.Allows(dest.String()) {
			t.Fatalf("an allowlist of %q does not allow its own destination %s", entry, dest)
		}

		allowed := allow.Allows(target)
		host, port, ok := strings.Cut(target, ":")
		if !ok || strings.Contains(port, ":") || strings.HasPrefix(target, "[") {
			return
		}
		// The same host in other case and with a root dot must get the same answer.
		for _, variant := range []string{strings.ToUpper(host) + ":" + port, host + ".:" + port} {
			if got := allow.Allows(variant); got != allowed {
				t.Fatalf("allowlist %q allows %q = %v but %q = %v", entry, target, allowed, variant, got)
			}
		}
		if allowed && !strings.EqualFold(strings.TrimRight(host, "."), dest.Host) {
			t.Fatalf("allowlist %q (%s) allowed a different host %q", entry, dest, target)
		}
	})
}
