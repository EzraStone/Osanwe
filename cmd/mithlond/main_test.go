package main

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/certs"
)

func TestCertNamesTrimsAndDefaults(t *testing.T) {
	tests := []struct {
		hosts string
		want  []string
	}{
		{"", []string{"localhost", "127.0.0.1"}},
		{" , ,", []string{"localhost", "127.0.0.1"}},
		{"gw.example", []string{"gw.example"}},
		{"gw.example, 10.0.0.5", []string{"gw.example", "10.0.0.5"}},
		{" gw.example ,,10.0.0.5 ", []string{"gw.example", "10.0.0.5"}},
	}
	for _, test := range tests {
		if got := certNames(test.hosts); !reflect.DeepEqual(got, test.want) {
			t.Errorf("certNames(%q) = %q, want %q", test.hosts, got, test.want)
		}
	}
}

// The point of trimming: an address written after a comma and a space must
// end up as an IP entry a client can verify against, not as a DNS name.
func TestSpacedHostsYieldAnIPAddressEntry(t *testing.T) {
	dir := t.TempDir()
	cert, _, _, err := certs.LoadOrCreate(filepath.Join(dir, "gw.crt"), filepath.Join(dir, "gw.key"),
		certNames("gw.example, 10.0.0.5"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.Leaf.VerifyHostname("10.0.0.5"); err != nil {
		t.Fatalf("certificate does not cover 10.0.0.5: %v (DNS %q, IP %v)", err, cert.Leaf.DNSNames, cert.Leaf.IPAddresses)
	}
	for _, name := range cert.Leaf.DNSNames {
		if name != "gw.example" {
			t.Errorf("unexpected DNS name %q", name)
		}
	}
}
