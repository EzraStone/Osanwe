package directory

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Descriptors arrive at authorities from anyone on the network. Whatever the
// bytes, parsing must not panic, and anything it accepts must be internally
// valid and survive a re-encode and re-parse unchanged.
func FuzzParseDescriptor(f *testing.F) {
	id, err := GenerateIdentity()
	if err != nil {
		f.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	d := &Descriptor{
		Nickname: "seed", Address: "relay.example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 43) + "=",
		Identity:     id.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Contact:      "ops@relay.example",
		Published:    now, Expires: now.Add(24 * time.Hour),
	}
	signed, err := d.Sign(id)
	if err != nil {
		f.Fatal(err)
	}
	if _, err := ParseDescriptor(signed); err != nil {
		f.Fatalf("the seed descriptor does not parse: %v", err)
	}
	f.Add(signed)
	f.Add([]byte(""))
	f.Add([]byte(descriptorV + "\n"))
	f.Add(signed[:len(signed)/2])
	f.Add(append(append([]byte(nil), signed...), "trailing\n"...))

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseDescriptor(data)
		if err != nil {
			return
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("accepted a descriptor that does not validate: %v\n%q", err, data)
		}
		again, err := ParseDescriptor(got.Encoded())
		if err != nil {
			t.Fatalf("an accepted descriptor did not survive re-encoding: %v\n%q", err, data)
		}
		for _, pair := range [][2]any{
			{got.Nickname, again.Nickname}, {got.Address, again.Address}, {got.TLSPin, again.TLSPin},
			{got.Identity, again.Identity}, {got.Contact, again.Contact},
			{got.Destinations, again.Destinations},
			{got.Published.Unix(), again.Published.Unix()}, {got.Expires.Unix(), again.Expires.Unix()},
		} {
			if !reflect.DeepEqual(pair[0], pair[1]) {
				t.Fatalf("re-parsing changed a field: %v became %v\n%q", pair[0], pair[1], data)
			}
		}
	})
}

// A client parses whatever a directory URL returns, before it knows whether
// the server is honest. Parsing must not panic, and anything accepted must
// carry at least the threshold of valid authority signatures, be in force at
// the time given, and list only valid relays.
func FuzzParseConsensus(f *testing.F) {
	var signers []*Identity
	var fingerprints []string
	for i := 0; i < 3; i++ {
		id, err := GenerateIdentity()
		if err != nil {
			f.Fatal(err)
		}
		signers = append(signers, id)
		fingerprints = append(fingerprints, id.Fingerprint())
	}
	set, err := AuthoritySet(fingerprints)
	if err != nil {
		f.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	relayID, err := GenerateIdentity()
	if err != nil {
		f.Fatal(err)
	}
	r := &Descriptor{
		Nickname: "alpha", Address: "alpha.example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 43) + "=",
		Identity:     relayID.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Published:    now.Add(-time.Hour), Expires: now.Add(24 * time.Hour),
	}
	if _, err := r.Sign(relayID); err != nil {
		f.Fatal(err)
	}
	c := &Consensus{ValidAfter: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), Relays: []*Descriptor{r}}
	for _, id := range signers[:2] {
		if err := c.Sign(id); err != nil {
			f.Fatal(err)
		}
	}
	encoded, err := c.Encode()
	if err != nil {
		f.Fatal(err)
	}
	if _, err := ParseConsensus(encoded, set, 2, now); err != nil {
		f.Fatalf("the seed consensus does not parse: %v", err)
	}
	f.Add(encoded)
	f.Add([]byte(""))
	f.Add(encoded[:len(encoded)/2])
	f.Add(append(append([]byte(nil), encoded...), "signature ed25519:x y\n"...))

	const threshold = 2
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseConsensus(data, set, threshold, now)
		if err != nil {
			return
		}
		if got.VerifiedBy() < threshold {
			t.Fatalf("accepted a consensus verified by %d authorities, below the threshold of %d\n%q", got.VerifiedBy(), threshold, data)
		}
		if now.Before(got.ValidAfter) || !now.Before(got.ValidUntil) {
			t.Fatalf("accepted a consensus not in force at %s (%s to %s)\n%q", now, got.ValidAfter, got.ValidUntil, data)
		}
		for _, relay := range got.Relays {
			if err := relay.Validate(); err != nil {
				t.Fatalf("accepted a consensus listing an invalid relay: %v\n%q", err, data)
			}
		}
	})
}
