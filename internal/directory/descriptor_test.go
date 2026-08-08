package directory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDescriptor(t *testing.T, id *Identity) *Descriptor {
	t.Helper()
	now := time.Now()
	return &Descriptor{
		Nickname:     "northrelay",
		Address:      "relay.example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     id.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Contact:      "ops@example",
		Published:    now,
		Expires:      now.Add(24 * time.Hour),
	}
}

func TestSignAndParseRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	d := testDescriptor(t, id)

	encoded, err := d.Sign(id)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	got, err := ParseDescriptor(encoded)
	if err != nil {
		t.Fatalf("ParseDescriptor: %v", err)
	}
	if got.Nickname != d.Nickname || got.Address != d.Address || got.TLSPin != d.TLSPin {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if got.Identity != id.Fingerprint() {
		t.Errorf("identity = %q, want %q", got.Identity, id.Fingerprint())
	}
	if len(got.Destinations) != 1 || got.Destinations[0] != "api.anthropic.com:443" {
		t.Errorf("destinations = %v", got.Destinations)
	}
	if got.Contact != "ops@example" {
		t.Errorf("contact = %q", got.Contact)
	}
	if !got.Published.Equal(d.Published.UTC().Truncate(time.Second)) {
		t.Errorf("published = %v, want %v", got.Published, d.Published)
	}
}

func TestDescriptorValidityAcrossConsensusWindow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	d := &Descriptor{
		Published: now.Add(-time.Hour),
		Expires:   now.Add(4 * time.Hour),
	}
	if !d.ValidThroughout(now, now.Add(3*time.Hour)) {
		t.Fatal("descriptor valid for the complete consensus window was rejected")
	}
	if d.ValidThroughout(now, now.Add(5*time.Hour)) {
		t.Fatal("descriptor expiring inside the consensus window was accepted")
	}
	future := &Descriptor{Published: now.Add(clockSkew + time.Second), Expires: now.Add(4 * time.Hour)}
	if future.ValidThroughout(now, now.Add(3*time.Hour)) {
		t.Fatal("descriptor published beyond the clock-skew allowance was accepted")
	}
	if d.ValidThroughout(now, now) {
		t.Fatal("empty consensus window was accepted")
	}
}

// TestAnyMutationInvalidatesTheSignature is the important one. Signing covers
// the exact received bytes, so changing any byte of the body must break it.
func TestAnyMutationInvalidatesTheSignature(t *testing.T) {
	id, _ := GenerateIdentity()
	d := testDescriptor(t, id)
	encoded, err := d.Sign(id)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	mutations := map[string][]byte{
		"changed address":     bytes.Replace(encoded, []byte("relay.example:8443"), []byte("evil.example:8443"), 1),
		"changed pin":         bytes.Replace(encoded, []byte("sha256/AAAA"), []byte("sha256/BBBB"), 1),
		"changed nickname":    bytes.Replace(encoded, []byte("northrelay"), []byte("southrelay"), 1),
		"changed expiry":      bytes.Replace(encoded, []byte("expires "), []byte("expires 2099-01-01T00:00:00Z\nx "), 1),
		"added destination":   bytes.Replace(encoded, []byte("contact "), []byte("destination evil.example:443\ncontact "), 1),
		"removed contact":     bytes.Replace(encoded, []byte("contact ops@example\n"), []byte(""), 1),
		"whitespace injected": bytes.Replace(encoded, []byte("nickname northrelay"), []byte("nickname  northrelay"), 1),
	}

	for name, mutated := range mutations {
		if bytes.Equal(mutated, encoded) {
			t.Fatalf("%s: mutation did not change the document; the test is not testing anything", name)
		}
		if _, err := ParseDescriptor(mutated); err == nil {
			t.Errorf("%s: mutated descriptor was accepted", name)
		}
	}
}

func TestParseRejectsSignatureFromAnotherKey(t *testing.T) {
	signer, _ := GenerateIdentity()
	other, _ := GenerateIdentity()

	// A descriptor advertising `other` but signed by `signer`. Sign refuses to
	// build it, so construct the bytes by hand as an attacker would.
	d := testDescriptor(t, signer)
	body := d.body()
	forged := append(append([]byte(nil), body...), []byte("signature "+signer.Sign(body)+"\n")...)
	forged = bytes.Replace(forged, []byte(signer.Fingerprint()), []byte(other.Fingerprint()), 1)

	if _, err := ParseDescriptor(forged); err == nil {
		t.Fatal("accepted a descriptor whose declared identity did not sign it")
	}
}

func TestSignRefusesIdentityMismatch(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()

	d := testDescriptor(t, a)
	if _, err := b.signDescriptor(d); err == nil {
		t.Fatal("signed a descriptor claiming somebody else's identity")
	}
}

// signDescriptor is a small helper so the mismatch test reads naturally.
func (id *Identity) signDescriptor(d *Descriptor) ([]byte, error) { return d.Sign(id) }

func TestParseRejectsUnknownFields(t *testing.T) {
	// Unknown fields must not be skipped: an old client would otherwise
	// report a document as valid while ignoring something that changed its
	// meaning.
	id, _ := GenerateIdentity()
	d := testDescriptor(t, id)

	body := append(d.body(), []byte("future-policy deny-everything\n")...)
	signed := append(append([]byte(nil), body...), []byte("signature "+id.Sign(body)+"\n")...)

	if _, err := ParseDescriptor(signed); err == nil {
		t.Fatal("accepted a validly signed descriptor containing an unknown field")
	}
}

func TestParseRejectsDuplicateFields(t *testing.T) {
	id, _ := GenerateIdentity()
	d := testDescriptor(t, id)

	body := bytes.Replace(d.body(),
		[]byte("address relay.example:8443\n"),
		[]byte("address relay.example:8443\naddress evil.example:8443\n"), 1)
	signed := append(append([]byte(nil), body...), []byte("signature "+id.Sign(body)+"\n")...)

	if _, err := ParseDescriptor(signed); err == nil {
		t.Fatal("accepted a descriptor with two address lines; meaning must never depend on which one a reader picks")
	}
}

func TestParseRejectsMalformedDocuments(t *testing.T) {
	id, _ := GenerateIdentity()
	d := testDescriptor(t, id)
	good, _ := d.Sign(id)

	cases := map[string][]byte{
		"empty":            {},
		"no signature":     d.body(),
		"signature only":   []byte("signature abcd\n"),
		"wrong version":    bytes.Replace(good, []byte(descriptorV), []byte("osanwe-descriptor-99"), 1),
		"empty signature":  bytes.Replace(good, []byte("signature "+d.signature), []byte("signature "), 1),
		"garbage":          []byte("this is not a descriptor at all\n"),
		"truncated body":   good[:len(good)/2],
		"bad base64 sig":   bytes.Replace(good, []byte(d.signature), []byte("!!!not-base64!!!"), 1),
		"missing line sep": bytes.ReplaceAll(good, []byte("\n"), []byte(" ")),
		// Anything after the signature is unsigned; tolerating it would make
		// two byte-different documents both verify.
		"trailing unsigned data": append(append([]byte(nil), good...), []byte("destination evil.example:443\n")...),
		"second signature line":  append(append([]byte(nil), good...), []byte("signature "+d.signature+"\n")...),
	}
	for name, data := range cases {
		if _, err := ParseDescriptor(data); err == nil {
			t.Errorf("%s: accepted a malformed document", name)
		}
	}
}

func TestValidateRejectsNonsense(t *testing.T) {
	id, _ := GenerateIdentity()
	base := testDescriptor(t, id)

	cases := map[string]func(*Descriptor){
		"no nickname":        func(d *Descriptor) { d.Nickname = "" },
		"spaces in nickname": func(d *Descriptor) { d.Nickname = "north relay" },
		"address no port":    func(d *Descriptor) { d.Address = "relay.example" },
		"bad pin":            func(d *Descriptor) { d.TLSPin = "not-a-pin" },
		"bad identity":       func(d *Descriptor) { d.Identity = "ed25519:zzz" },
		"no destinations":    func(d *Descriptor) { d.Destinations = nil },
		"no window":          func(d *Descriptor) { d.Published, d.Expires = time.Time{}, time.Time{} },
		"expires first":      func(d *Descriptor) { d.Expires = d.Published.Add(-time.Hour) },
	}
	for name, mutate := range cases {
		d := *base
		d.Destinations = append([]string(nil), base.Destinations...)
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: Validate accepted a nonsense descriptor", name)
		}
	}
}

func TestExpiry(t *testing.T) {
	id, _ := GenerateIdentity()
	now := time.Now()
	d := &Descriptor{Published: now.Add(-time.Hour), Expires: now.Add(time.Hour)}
	_ = id

	if d.Expired(now) {
		t.Error("descriptor inside its window reported expired")
	}
	if !d.Expired(now.Add(2 * time.Hour)) {
		t.Error("descriptor past its expiry reported valid; a stale descriptor would be replayable forever")
	}
	// Modest clock skew must be tolerated, or a client with a fast clock
	// rejects a descriptor published seconds ago.
	if d.Expired(d.Published.Add(-time.Minute)) {
		t.Error("descriptor rejected for a one-minute clock skew")
	}
	if !d.Expired(d.Published.Add(-time.Hour)) {
		t.Error("descriptor accepted an hour before it was published")
	}
}

func TestServes(t *testing.T) {
	d := &Descriptor{Destinations: []string{"api.anthropic.com:443", "api.openai.com:443"}}
	for _, want := range []string{"api.anthropic.com:443", "API.ANTHROPIC.COM:443", "  api.openai.com:443 "} {
		if !d.Serves(want) {
			t.Errorf("Serves(%q) = false", want)
		}
	}
	for _, no := range []string{"evil.example:443", "api.anthropic.com:8443", "api.anthropic.com"} {
		if d.Serves(no) {
			t.Errorf("Serves(%q) = true", no)
		}
	}
}

func TestIdentityRoundTripOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.key")

	id, created, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	if !created {
		t.Error("first call reported created=false")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity file mode = %o, want 600", perm)
	}

	again, created2, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateIdentity: %v", err)
	}
	if created2 {
		t.Error("second call regenerated an existing identity")
	}
	if again.Fingerprint() != id.Fingerprint() {
		t.Error("identity changed across reload; every client that pinned it would break")
	}

	if err := WriteIdentity(id, path); err == nil {
		t.Error("WriteIdentity overwrote an existing identity")
	}
}

func TestKeyEncoding(t *testing.T) {
	id, _ := GenerateIdentity()
	fp := id.Fingerprint()
	if !strings.HasPrefix(fp, KeyPrefix) {
		t.Errorf("fingerprint %q lacks the %q prefix", fp, KeyPrefix)
	}

	for _, in := range []string{fp, strings.TrimPrefix(fp, KeyPrefix), "  " + fp + "  "} {
		got, err := DecodeKey(in)
		if err != nil {
			t.Errorf("DecodeKey(%q): %v", in, err)
			continue
		}
		if !got.Equal(id.Public) {
			t.Errorf("DecodeKey(%q) returned the wrong key", in)
		}
	}
	for _, bad := range []string{"", "   ", "ed25519:!!!", "ed25519:AAAA"} {
		if _, err := DecodeKey(bad); err == nil {
			t.Errorf("DecodeKey(%q) succeeded, want error", bad)
		}
	}
}
