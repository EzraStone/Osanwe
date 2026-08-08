package directory

import (
	"bufio"
	"crypto/ed25519"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/EzraStone/osanwe/internal/certs"
)

// Documents are line-oriented text, signed over the exact bytes of the body.
//
// Verification never re-serialises a parsed struct and compares. That pattern
// is the source of a whole family of signature-bypass bugs: any disagreement
// between parser and serialiser -- a field the parser ignores, a value it
// normalises, duplicate keys resolved differently -- becomes a way to change
// a document's meaning while keeping a valid signature. Here the signature
// covers the received bytes and nothing else, so a document either arrived
// exactly as signed or it is rejected.
const (
	signatureKey = "signature"
	descriptorV  = "osanwe-descriptor-1"
)

// Descriptor is a relay's self-published statement about itself.
//
// It is signed by the relay's own identity key, which makes it self-certifying:
// anyone holding the relay's fingerprint can verify a descriptor without
// trusting whoever handed it over, including the directory.
type Descriptor struct {
	Nickname     string
	Address      string   // host:port
	TLSPin       string   // sha256/... -- what a client checks during the handshake
	Identity     string   // ed25519:... -- the signer
	Destinations []string // what this relay will carry traffic to
	Contact      string   // optional operator contact
	Published    time.Time
	Expires      time.Time

	// raw holds the exact signed bytes when this descriptor came from Parse,
	// so a re-encode is never substituted for what was verified.
	raw       []byte
	signature string
}

// Signature returns the descriptor's signature, empty if unsigned.
func (d *Descriptor) Signature() string { return d.signature }

// Raw returns the exact bytes that were signed, for a caller that wants to
// relay a descriptor onward without disturbing its signature.
func (d *Descriptor) Raw() []byte {
	if d.raw == nil {
		return nil
	}
	return append([]byte(nil), d.raw...)
}

// Validate checks a descriptor's fields for internal sense, independent of any
// signature. A well-signed nonsense descriptor is still nonsense.
func (d *Descriptor) Validate() error {
	if d.Nickname == "" {
		return fmt.Errorf("directory: descriptor has no nickname")
	}
	if strings.ContainsAny(d.Nickname, " \t\r\n") {
		return fmt.Errorf("directory: nickname %q contains whitespace", d.Nickname)
	}
	if _, _, err := net.SplitHostPort(d.Address); err != nil {
		return fmt.Errorf("directory: descriptor address %q must be host:port: %w", d.Address, err)
	}
	if _, err := certs.NormalizePin(d.TLSPin); err != nil {
		return fmt.Errorf("directory: descriptor %q: %w", d.Nickname, err)
	}
	if _, err := DecodeKey(d.Identity); err != nil {
		return fmt.Errorf("directory: descriptor %q: %w", d.Nickname, err)
	}
	if len(d.Destinations) == 0 {
		return fmt.Errorf("directory: descriptor %q lists no destinations, so no client could use it", d.Nickname)
	}
	if d.Published.IsZero() || d.Expires.IsZero() {
		return fmt.Errorf("directory: descriptor %q has no validity window", d.Nickname)
	}
	if !d.Expires.After(d.Published) {
		return fmt.Errorf("directory: descriptor %q expires at or before it was published", d.Nickname)
	}
	return nil
}

// Expired reports whether the descriptor is outside its validity window.
//
// Expiry is what stops an old descriptor being replayed after a relay's key is
// rotated or its operator withdraws it. Without it a stale descriptor would be
// valid forever, since its signature never stops verifying.
func (d *Descriptor) Expired(now time.Time) bool {
	return now.After(d.Expires) || now.Before(d.Published.Add(-clockSkew))
}

// ValidThroughout reports whether a descriptor is usable for an entire
// consensus window. Authorities evaluate this against deterministic epoch
// boundaries, not their individual wall-clock sampling times, so identical
// descriptor directories produce identical relay sets throughout an epoch.
func (d *Descriptor) ValidThroughout(validAfter, validUntil time.Time) bool {
	if validAfter.IsZero() || validUntil.IsZero() || !validUntil.After(validAfter) {
		return false
	}
	return !d.Expired(validAfter) && !validUntil.After(d.Expires)
}

// clockSkew is the tolerance applied to validity windows, so a client whose
// clock is a little fast does not reject a descriptor published seconds ago.
const clockSkew = 5 * time.Minute

// Serves reports whether this relay will carry traffic to a destination.
func (d *Descriptor) Serves(hostPort string) bool {
	want := strings.ToLower(strings.TrimSpace(hostPort))
	for _, dest := range d.Destinations {
		if strings.ToLower(strings.TrimSpace(dest)) == want {
			return true
		}
	}
	return false
}

// body renders the signed portion. Field order is fixed and destinations are
// sorted, so the same descriptor always produces the same bytes.
func (d *Descriptor) body() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", descriptorV)
	fmt.Fprintf(&b, "nickname %s\n", d.Nickname)
	fmt.Fprintf(&b, "address %s\n", d.Address)
	fmt.Fprintf(&b, "tls-pin %s\n", d.TLSPin)
	fmt.Fprintf(&b, "identity %s\n", d.Identity)

	dests := append([]string(nil), d.Destinations...)
	sort.Strings(dests)
	for _, dest := range dests {
		fmt.Fprintf(&b, "destination %s\n", dest)
	}
	if d.Contact != "" {
		fmt.Fprintf(&b, "contact %s\n", d.Contact)
	}
	fmt.Fprintf(&b, "published %s\n", d.Published.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "expires %s\n", d.Expires.UTC().Format(time.RFC3339))
	return []byte(b.String())
}

// Sign returns the descriptor encoded and signed by id.
//
// The identity field must match the signing key: a descriptor claiming one
// identity and signed by another would verify against the signer's key while
// advertising somebody else's name.
func (d *Descriptor) Sign(id *Identity) ([]byte, error) {
	if d.Identity == "" {
		d.Identity = id.Fingerprint()
	}
	if d.Identity != id.Fingerprint() {
		return nil, fmt.Errorf("directory: descriptor claims identity %s but is being signed by %s",
			d.Identity, id.Fingerprint())
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}

	body := d.body()
	sig := id.Sign(body)
	d.raw = body
	d.signature = sig

	out := append([]byte(nil), body...)
	out = append(out, []byte(signatureKey+" "+sig+"\n")...)
	return out, nil
}

// ParseDescriptor decodes and verifies a descriptor.
//
// The signature is checked against the identity the descriptor itself declares,
// which proves only that the descriptor was written by the holder of that key.
// Deciding whether that key belongs to a relay worth using is a separate
// question, answered by a pin the user already had or by a consensus.
func ParseDescriptor(data []byte) (*Descriptor, error) {
	body, sig, err := splitSignature(data)
	if err != nil {
		return nil, err
	}

	d := &Descriptor{raw: body, signature: sig}
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	first := true
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if first {
			if line != descriptorV {
				return nil, fmt.Errorf("directory: not a descriptor, or unsupported version: %q", line)
			}
			first = false
			continue
		}

		key, value, found := strings.Cut(line, " ")
		if !found {
			return nil, fmt.Errorf("directory: malformed descriptor line %q", line)
		}
		value = strings.TrimSpace(value)

		// Duplicate single-valued keys are rejected rather than
		// last-one-wins. A parser that quietly accepts two "address" lines
		// invites a document whose meaning depends on which one a reader
		// happens to use.
		if key != "destination" {
			if seen[key] {
				return nil, fmt.Errorf("directory: duplicate %q field in descriptor", key)
			}
			seen[key] = true
		}

		switch key {
		case "nickname":
			d.Nickname = value
		case "address":
			d.Address = value
		case "tls-pin":
			d.TLSPin = value
		case "identity":
			d.Identity = value
		case "destination":
			d.Destinations = append(d.Destinations, value)
		case "contact":
			d.Contact = value
		case "published":
			if d.Published, err = time.Parse(time.RFC3339, value); err != nil {
				return nil, fmt.Errorf("directory: bad published time %q: %w", value, err)
			}
		case "expires":
			if d.Expires, err = time.Parse(time.RFC3339, value); err != nil {
				return nil, fmt.Errorf("directory: bad expires time %q: %w", value, err)
			}
		default:
			// Unknown fields are refused, not skipped. Skipping would let a
			// future field carrying real meaning be ignored by an old client
			// that still reports the document as valid.
			return nil, fmt.Errorf("directory: unknown descriptor field %q", key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("directory: reading descriptor: %w", err)
	}
	if first {
		return nil, fmt.Errorf("directory: empty descriptor")
	}

	if err := d.Validate(); err != nil {
		return nil, err
	}

	pub, err := DecodeKey(d.Identity)
	if err != nil {
		return nil, err
	}
	if !VerifySignature(pub, body, sig) {
		return nil, fmt.Errorf("directory: descriptor %q has an invalid signature", d.Nickname)
	}
	return d, nil
}

// splitSignature separates the signed body from the trailing signature line.
// The body returned is exactly the bytes preceding that line.
func splitSignature(data []byte) (body []byte, sig string, err error) {
	text := string(data)
	idx := strings.LastIndex(text, "\n"+signatureKey+" ")
	if idx < 0 {
		if strings.HasPrefix(text, signatureKey+" ") {
			return nil, "", fmt.Errorf("directory: document has a signature but no body")
		}
		return nil, "", fmt.Errorf("directory: document has no signature line")
	}

	body = []byte(text[:idx+1]) // include the newline the body ends with
	rest := text[idx+1:]

	line, after, _ := strings.Cut(rest, "\n")
	sig = strings.TrimSpace(strings.TrimPrefix(line, signatureKey+" "))
	if sig == "" {
		return nil, "", fmt.Errorf("directory: empty signature")
	}

	// The signature must be the last thing in the document. Anything after it
	// is unsigned, and tolerating it would make documents malleable: two
	// byte-different files would both verify, and a reader that looked past
	// the signature line could be shown content the signer never approved.
	if strings.TrimSpace(after) != "" {
		return nil, "", fmt.Errorf("directory: %d unsigned bytes follow the signature line", len(after))
	}
	return body, sig, nil
}

// VerifyDescriptorAgainst checks that a descriptor was signed by an expected
// identity, for a client that already knows which relay it is looking for.
func VerifyDescriptorAgainst(d *Descriptor, expected ed25519.PublicKey) error {
	got, err := DecodeKey(d.Identity)
	if err != nil {
		return err
	}
	if !got.Equal(expected) {
		return fmt.Errorf("directory: descriptor %q is signed by %s, not the expected identity",
			d.Nickname, d.Identity)
	}
	return nil
}
