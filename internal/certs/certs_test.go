package certs

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelfSignedProducesUsableIdentity(t *testing.T) {
	cert, pin, err := SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("SelfSigned returned no parsed leaf")
	}
	if !strings.HasPrefix(pin, PinPrefix) {
		t.Errorf("pin %q lacks the %q prefix", pin, PinPrefix)
	}
	if got := Pin(cert.Leaf); got != pin {
		t.Errorf("Pin(leaf) = %q, want %q", got, pin)
	}

	if len(cert.Leaf.DNSNames) != 1 || cert.Leaf.DNSNames[0] != "localhost" {
		t.Errorf("DNSNames = %v, want [localhost]", cert.Leaf.DNSNames)
	}
	if len(cert.Leaf.IPAddresses) != 1 || cert.Leaf.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("IPAddresses = %v, want [127.0.0.1]", cert.Leaf.IPAddresses)
	}
	// Skew tolerance: a relay whose clock is a few minutes fast must not
	// present a certificate that is not yet valid.
	if !cert.Leaf.NotBefore.Before(time.Now()) {
		t.Error("NotBefore is not in the past; clock skew would break new relays")
	}
}

func TestPinIsStableAcrossCertificatesWithSameKey(t *testing.T) {
	// Renewing a certificate with the same key must not change the pin, or
	// every client would break on renewal.
	cert, pin, err := SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}

	reparsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if Pin(reparsed) != pin {
		t.Error("pin changed when the same certificate was reparsed")
	}
}

func TestDistinctIdentitiesHaveDistinctPins(t *testing.T) {
	_, a, err := SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	_, b, err := SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	if a == b {
		t.Fatal("two freshly generated identities produced the same pin")
	}
}

func TestNormalizePin(t *testing.T) {
	_, pin, err := SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	bare := strings.TrimPrefix(pin, PinPrefix)

	for _, in := range []string{pin, bare, "  " + pin + "  ", "  " + bare} {
		got, err := NormalizePin(in)
		if err != nil {
			t.Errorf("NormalizePin(%q): %v", in, err)
			continue
		}
		if got != pin {
			t.Errorf("NormalizePin(%q) = %q, want %q", in, got, pin)
		}
	}

	for _, bad := range []string{"", "   ", "not base64!!", "sha256/short", "sha256/" + bare[:10]} {
		if _, err := NormalizePin(bad); err == nil {
			t.Errorf("NormalizePin(%q) succeeded, want error", bad)
		}
	}
}

func TestWriteAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ranger.crt")
	keyPath := filepath.Join(dir, "ranger.key")

	cert, pin, err := SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	if err := WritePEM(cert, certPath, keyPath); err != nil {
		t.Fatalf("WritePEM: %v", err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600; a relay's private key must not be world-readable", perm)
	}

	loaded, loadedPin, err := Load(certPath, keyPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loadedPin != pin {
		t.Errorf("pin after round trip = %q, want %q", loadedPin, pin)
	}
	if loaded.Leaf == nil {
		t.Error("Load did not populate Leaf")
	}
}

func TestWritePEMRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ranger.crt")
	keyPath := filepath.Join(dir, "ranger.key")

	cert, _, err := SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	if err := WritePEM(cert, certPath, keyPath); err != nil {
		t.Fatalf("first WritePEM: %v", err)
	}

	other, _, err := SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	if err := WritePEM(other, certPath, keyPath); err == nil {
		t.Fatal("WritePEM overwrote an existing identity; that would silently invalidate every client's pin")
	}
}

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ranger.crt")
	keyPath := filepath.Join(dir, "ranger.key")

	_, pin, created, err := LoadOrCreate(certPath, keyPath, []string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	if !created {
		t.Error("first LoadOrCreate reported created=false")
	}

	// The identity must survive a restart, or the relay would present a new
	// pin every time it started and break every client.
	_, pin2, created2, err := LoadOrCreate(certPath, keyPath, []string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if created2 {
		t.Error("second LoadOrCreate regenerated an identity that already existed")
	}
	if pin2 != pin {
		t.Errorf("pin changed across restart: %q then %q", pin, pin2)
	}
}

func TestGeneratedCertificateCompletesTLSHandshake(t *testing.T) {
	cert, pin, err := SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// The handshake is lazy: closing before completing it would fail the
		// client's Dial with EOF regardless of whether the certificate works.
		if tc, ok := conn.(*tls.Conn); ok {
			_ = tc.Handshake()
		}
	}()

	var seen string
	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true, // verification here is by pin, below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			seen = Pin(leaf)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	if seen != pin {
		t.Errorf("pin observed over the wire = %q, want %q", seen, pin)
	}
}
