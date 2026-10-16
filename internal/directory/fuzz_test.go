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
