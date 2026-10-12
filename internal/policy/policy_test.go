package policy

import (
	"strings"
	"testing"
)

func TestParseRejectsEmptyList(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatal("Parse(nil) succeeded; an empty allowlist must be an error, not a relay that silently refuses everything")
	}
}

func TestParseDestination(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{in: "api.anthropic.com", wantHost: "api.anthropic.com", wantPort: 443},
		{in: "api.anthropic.com:443", wantHost: "api.anthropic.com", wantPort: 443},
		{in: "api.anthropic.com:8443", wantHost: "api.anthropic.com", wantPort: 8443},
		{in: "  api.anthropic.com  ", wantHost: "api.anthropic.com", wantPort: 443},

		// Normalisation: these must all collapse to one entry, or an operator
		// can believe a host is blocked when it is reachable.
		{in: "API.Anthropic.COM", wantHost: "api.anthropic.com", wantPort: 443},
		{in: "api.anthropic.com.", wantHost: "api.anthropic.com", wantPort: 443},
		{in: "api.anthropic.com...", wantHost: "api.anthropic.com", wantPort: 443},

		{in: "127.0.0.1:8080", wantHost: "127.0.0.1", wantPort: 8080},
		{in: "[::1]:8080", wantHost: "::1", wantPort: 8080},

		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "host:notaport", wantErr: true},
		{in: "host:0", wantErr: true},
		{in: "host:65536", wantErr: true},
		{in: "host:-1", wantErr: true},
		{in: ":443", wantErr: true},
	}

	for _, tt := range tests {
		got, err := ParseDestination(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseDestination(%q) = %v, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDestination(%q) returned unexpected error: %v", tt.in, err)
			continue
		}
		if got.Host != tt.wantHost || got.Port != tt.wantPort {
			t.Errorf("ParseDestination(%q) = %s:%d, want %s:%d",
				tt.in, got.Host, got.Port, tt.wantHost, tt.wantPort)
		}
	}
}

// Entries a CONNECT target could never equal must be refused at parse time:
// accepted, they start a relay that silently refuses that destination.
func TestParseDestinationRejectsWhatCouldNeverMatch(t *testing.T) {
	tests := map[string]string{
		"https://api.anthropic.com":    "is a URL",
		"https://api.anthropic.com/v1": "is a URL",
		"api.anthropic.com/v1":         "path, query or credentials",
		"api.anthropic.com:443/v1":     "path, query or credentials",
		"user@api.anthropic.com":       "path, query or credentials",
		"api.anthropic.com?x=1":        "path, query or credentials",
		"api.anthropic.com#frag":       "path, query or credentials",
		"api anthropic.com":            "not a host name",
		"api..anthropic.com":           "not a host name",
		".api.anthropic.com":           "not a host name",
		"api.anthropic.com\x00":        "not a host name",
		strings.Repeat("a", 254):       "not a host name",
	}
	for in, want := range tests {
		_, err := ParseDestination(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseDestination(%q) error = %v, want one containing %q", in, err, want)
		}
	}
}

func TestParseDestinationKeepsLegitimateNames(t *testing.T) {
	for in, want := range map[string]string{
		"internal_gw.corp.example:8443": "internal_gw.corp.example",
		"xn--bcher-kva.example":         "xn--bcher-kva.example",
		"::1":                           "::1",
		"[::1]":                         "::1",
		"2001:db8::7":                   "2001:db8::7",
	} {
		d, err := ParseDestination(in)
		if err != nil || d.Host != want {
			t.Errorf("ParseDestination(%q) = %+v, %v; want host %q", in, d, err, want)
		}
	}
}

func TestAllowsDefaultDeny(t *testing.T) {
	a, err := Parse([]string{"api.anthropic.com"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	allowed := []string{
		"api.anthropic.com:443",
		"API.ANTHROPIC.COM:443", // case is normalised
		"api.anthropic.com.:443",
	}
	for _, s := range allowed {
		if !a.Allows(s) {
			t.Errorf("Allows(%q) = false, want true", s)
		}
	}

	denied := []string{
		"evil.example.com:443",   // not listed
		"api.anthropic.com:8443", // right host, wrong port
		"api.anthropic.com",      // no port: must not be smuggled past
		"api.anthropic.com:80",   // plaintext port
		"",
		"garbage",
		"host:notaport",
		"sub.api.anthropic.com:443", // subdomains are not implied
	}
	for _, s := range denied {
		if a.Allows(s) {
			t.Errorf("Allows(%q) = true, want false", s)
		}
	}
}

func TestZeroValueAndNilAllowNothing(t *testing.T) {
	var nilList *Allowlist
	if nilList.Allows("api.anthropic.com:443") {
		t.Error("nil *Allowlist allowed a destination; the zero value must permit nothing")
	}
	if nilList.Len() != 0 {
		t.Error("nil *Allowlist reported a non-zero length")
	}

	empty := &Allowlist{}
	if empty.Allows("api.anthropic.com:443") {
		t.Error("zero-value Allowlist allowed a destination; it must permit nothing")
	}
}

func TestPortOnlyEntryDoesNotWidenHost(t *testing.T) {
	// Listing one port must not implicitly permit the same host on another.
	a, err := Parse([]string{"api.anthropic.com:443"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, p := range []string{":22", ":80", ":8080", ":9999"} {
		if a.Allows("api.anthropic.com" + p) {
			t.Errorf("Allows(api.anthropic.com%s) = true; listing :443 must not widen to other ports", p)
		}
	}
}

func TestDestinationsSorted(t *testing.T) {
	a, err := Parse([]string{"b.example:443", "a.example:443", "a.example:8443"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := a.Destinations()
	want := []string{"a.example:443", "a.example:8443", "b.example:443"}
	if len(got) != len(want) {
		t.Fatalf("Destinations() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Destinations() = %v, want %v", got, want)
		}
	}
}

func TestDuplicateEntriesCollapse(t *testing.T) {
	a, err := Parse([]string{"api.anthropic.com", "api.anthropic.com:443", "API.ANTHROPIC.COM."})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Len() != 1 {
		t.Errorf("Len() = %d, want 1; equivalent spellings must collapse to one entry", a.Len())
	}
}
