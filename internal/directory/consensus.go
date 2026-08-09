package directory

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const consensusV = "osanwe-consensus-1"

// A consensus lists the relays a set of directory authorities agree exist,
// signed by several of them.
//
// The threshold is the whole point. A single-authority directory would mean one
// compromised or coerced machine could hand every client a relay it controls,
// with a correct-looking pin, and clients would connect to it happily. Since
// the descriptors inside are themselves signed by their relays, an authority
// cannot forge a relay's key -- but it could omit honest relays until only its
// own remained, which is the same attack with more steps. Requiring agreement
// from several independent authorities is what makes that expensive.
type Consensus struct {
	// ValidAfter and ValidUntil bound when clients will use this document.
	ValidAfter time.Time
	ValidUntil time.Time

	// Relays are the descriptors, already signature-verified by Parse.
	Relays []*Descriptor

	// Signatures maps an authority fingerprint to its signature over the body.
	//
	// This is every signature present on the document, including ones from keys
	// the verifier did not recognise. Do not count it to decide how much
	// agreement the consensus carries; use VerifiedBy.
	Signatures map[string]string

	verifiedBy int

	raw []byte
}

// VerifiedBy reports how many signatures came from authorities this client
// knows and were checked successfully.
//
// It is deliberately not len(Signatures). Unknown signatures are kept rather
// than rejected, so that adding an authority does not break clients that have
// not heard of it -- which means anyone can append signatures to a consensus
// and inflate that length without breaking a single check. Only this number
// describes how many independent parties actually vouched for the document a
// client is using.
func (c *Consensus) VerifiedBy() int { return c.verifiedBy }

// Raw returns the exact signed body, for forwarding a consensus unchanged.
func (c *Consensus) Raw() []byte {
	if c.raw == nil {
		return nil
	}
	return append([]byte(nil), c.raw...)
}

// Fresh reports whether the consensus is inside its validity window.
//
// Expiry is what stops an authority (or anyone who kept a copy) replaying an
// old consensus after a relay has been withdrawn or its key rotated. A
// signature alone never goes stale, so the document has to.
func (c *Consensus) Fresh(now time.Time) bool {
	return !now.Before(c.ValidAfter.Add(-clockSkew)) && now.Before(c.ValidUntil)
}

// Usable returns the relays that are fresh, serve the given destination, and
// are not otherwise unusable. Passing an empty destination skips that filter.
func (c *Consensus) Usable(now time.Time, destination string) []*Descriptor {
	var out []*Descriptor
	for _, d := range c.Relays {
		if d.Expired(now) {
			continue
		}
		if destination != "" && !d.Serves(destination) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// body renders the signed portion: the header, then each descriptor verbatim.
//
// Descriptors are embedded as their own signed bytes rather than re-encoded,
// so a relay's signature survives being carried inside a consensus and a client
// can check it independently of the authorities.
func (c *Consensus) body() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", consensusV)
	fmt.Fprintf(&b, "valid-after %s\n", c.ValidAfter.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "valid-until %s\n", c.ValidUntil.UTC().Format(time.RFC3339))

	relays := append([]*Descriptor(nil), c.Relays...)
	// Sort by the complete signed descriptor, not just the human-readable
	// nickname. Nicknames are not identities, and two operators can choose the
	// same one. A total byte ordering means authorities given the same set of
	// descriptors always construct the same body regardless of directory order.
	sort.Slice(relays, func(i, j int) bool {
		return bytes.Compare(relays[i].Encoded(), relays[j].Encoded()) < 0
	})

	for _, d := range relays {
		raw := d.Raw()
		if raw == nil {
			continue
		}
		encoded := base64.StdEncoding.EncodeToString(
			append(raw, []byte(signatureKey+" "+d.signature+"\n")...))
		fmt.Fprintf(&b, "relay %d %s\n", len(encoded), encoded)
	}
	return []byte(b.String())
}

// Sign adds one authority's signature. Building a consensus means calling this
// once per authority, on the same document.
func (c *Consensus) Sign(id *Identity) error {
	if id == nil || len(id.Private) != ed25519.PrivateKeySize {
		return fmt.Errorf("directory: cannot sign a consensus without a valid authority identity")
	}
	if err := c.validate(); err != nil {
		return err
	}
	body := c.body()
	if c.raw != nil && !bytes.Equal(c.raw, body) {
		return fmt.Errorf("directory: consensus changed after it was first signed; earlier signatures would be invalid")
	}
	c.raw = body
	if c.Signatures == nil {
		c.Signatures = map[string]string{}
	}
	c.Signatures[id.Fingerprint()] = id.Sign(body)
	return nil
}

// Encode renders the consensus with all its signatures.
func (c *Consensus) Encode() ([]byte, error) {
	if len(c.Signatures) == 0 {
		return nil, fmt.Errorf("directory: refusing to encode an unsigned consensus")
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	body := c.body()
	if c.raw != nil && !bytes.Equal(c.raw, body) {
		return nil, fmt.Errorf("directory: consensus changed after it was signed; refusing to invalidate its signatures")
	}
	out := append([]byte(nil), body...)

	fps := make([]string, 0, len(c.Signatures))
	for fp := range c.Signatures {
		fps = append(fps, fp)
	}
	sort.Strings(fps)
	for _, fp := range fps {
		pub, err := DecodeKey(fp)
		if err != nil || EncodeKey(pub) != fp {
			return nil, fmt.Errorf("directory: consensus signature has a non-canonical authority key %q", fp)
		}
		if !VerifySignature(pub, body, c.Signatures[fp]) {
			return nil, fmt.Errorf("directory: consensus signature from %s is invalid", fp)
		}
		out = append(out, []byte(signatureKey+" "+fp+" "+c.Signatures[fp]+"\n")...)
	}
	if len(out) > MaxConsensusSize {
		return nil, fmt.Errorf("directory: encoded consensus is %d bytes, larger than the %d-byte protocol limit", len(out), MaxConsensusSize)
	}
	return out, nil
}

// validate checks the parts of a consensus that must be true before anybody
// signs it. In particular, one relay identity may appear only once. Otherwise
// an authority could weight selection toward one operator by repeating its
// descriptor under several entries.
func (c *Consensus) validate() error {
	if c.ValidAfter.IsZero() || c.ValidUntil.IsZero() {
		return fmt.Errorf("directory: consensus has no validity window")
	}
	if !c.ValidUntil.After(c.ValidAfter) {
		return fmt.Errorf("directory: consensus expires at or before it becomes valid")
	}

	identities := make(map[string]struct{}, len(c.Relays))
	for i, d := range c.Relays {
		if d == nil {
			return fmt.Errorf("directory: consensus relay %d is nil", i)
		}
		encoded := d.Encoded()
		if len(encoded) == 0 {
			return fmt.Errorf("directory: relay %q is not a signed descriptor", d.Nickname)
		}
		if err := d.Validate(); err != nil {
			return fmt.Errorf("directory: relay %q is invalid: %w", d.Nickname, err)
		}
		if !d.ValidThroughout(c.ValidAfter, c.ValidUntil) {
			return fmt.Errorf("directory: relay %q is not valid throughout the consensus window %s to %s",
				d.Nickname, c.ValidAfter.UTC().Format(time.RFC3339), c.ValidUntil.UTC().Format(time.RFC3339))
		}
		if _, duplicate := identities[d.Identity]; duplicate {
			return fmt.Errorf("directory: relay identity %s appears more than once in the consensus", d.Identity)
		}
		identities[d.Identity] = struct{}{}
	}
	return nil
}

// ParseConsensus decodes a consensus and verifies it against a set of trusted
// authority keys, requiring at least `threshold` distinct valid signatures.
//
// The threshold is enforced here rather than left to the caller. A verify
// function that returns "valid" after checking one signature, and trusts the
// caller to count, is a function that will eventually be called without the
// counting.
func ParseConsensus(data []byte, authorities map[string]ed25519.PublicKey, threshold int, now time.Time) (*Consensus, error) {
	if threshold < 1 {
		return nil, fmt.Errorf("directory: threshold must be at least 1")
	}
	if len(authorities) < threshold {
		return nil, fmt.Errorf("directory: %d authorities configured but threshold is %d; no consensus could ever satisfy it",
			len(authorities), threshold)
	}

	body, sigs, err := splitConsensusSignatures(data)
	if err != nil {
		return nil, err
	}

	c := &Consensus{raw: body, Signatures: sigs}
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 0, 256*1024), 8<<20)

	first := true
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if first {
			if line != consensusV {
				return nil, fmt.Errorf("directory: not a consensus, or unsupported version: %q", line)
			}
			first = false
			continue
		}

		key, value, found := strings.Cut(line, " ")
		if !found {
			return nil, fmt.Errorf("directory: malformed consensus line %q", line)
		}

		switch key {
		case "valid-after", "valid-until":
			if seen[key] {
				return nil, fmt.Errorf("directory: duplicate %q in consensus", key)
			}
			seen[key] = true
			ts, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("directory: bad %s time %q: %w", key, value, err)
			}
			if key == "valid-after" {
				c.ValidAfter = ts
			} else {
				c.ValidUntil = ts
			}

		case "relay":
			lengthStr, encoded, ok := strings.Cut(strings.TrimSpace(value), " ")
			if !ok {
				return nil, fmt.Errorf("directory: malformed relay entry")
			}
			// The declared length must match, so a truncated or padded entry
			// is a parse error rather than a silently different descriptor.
			want, err := strconv.Atoi(lengthStr)
			if err != nil {
				return nil, fmt.Errorf("directory: relay entry has a non-numeric length %q", lengthStr)
			}
			if len(encoded) != want {
				return nil, fmt.Errorf("directory: relay entry declares %d bytes but carries %d", want, len(encoded))
			}
			rawDesc, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("directory: relay entry is not valid base64: %w", err)
			}
			// Each descriptor is verified against its OWN key here. An
			// authority that fabricated an entry would have to forge a relay's
			// signature, which it cannot do.
			d, err := ParseDescriptor(rawDesc)
			if err != nil {
				return nil, fmt.Errorf("directory: consensus contains an invalid descriptor: %w", err)
			}
			c.Relays = append(c.Relays, d)

		default:
			return nil, fmt.Errorf("directory: unknown consensus field %q", key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("directory: reading consensus: %w", err)
	}
	if first {
		return nil, fmt.Errorf("directory: empty consensus")
	}
	if c.ValidAfter.IsZero() || c.ValidUntil.IsZero() {
		return nil, fmt.Errorf("directory: consensus has no validity window")
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	// Consensus production is deliberately canonical. Exact-byte signatures
	// are still verified below, but refusing alternate whitespace, ordering and
	// time spellings prevents two authorities from accidentally signing two
	// byte-different bodies with identical meaning.
	if canonical := c.body(); !bytes.Equal(body, canonical) {
		return nil, fmt.Errorf("directory: consensus body is not in canonical order or encoding")
	}

	// Freshness before signatures: a correctly signed but expired consensus is
	// exactly what a replay looks like, and saying so is more useful than
	// reporting a signature problem that does not exist.
	if !c.Fresh(now) {
		return nil, fmt.Errorf("directory: consensus is not fresh (valid %s to %s, now %s); it may be a replayed copy",
			c.ValidAfter.UTC().Format(time.RFC3339),
			c.ValidUntil.UTC().Format(time.RFC3339),
			now.UTC().Format(time.RFC3339))
	}

	valid := 0
	for fp, sig := range sigs {
		pub, known := authorities[fp]
		if !known {
			// Signatures from unknown keys are ignored, not fatal: an
			// authority being added to the network should not break clients
			// that have not heard of it yet.
			continue
		}
		if VerifySignature(pub, body, sig) {
			valid++
		}
	}
	if valid < threshold {
		return nil, fmt.Errorf("directory: consensus carries %d valid signatures from known authorities, need %d",
			valid, threshold)
	}
	c.verifiedBy = valid
	return c, nil
}

// splitConsensusSignatures separates the body from its trailing signature
// lines, each of the form "signature <fingerprint> <sig>".
func splitConsensusSignatures(data []byte) (body []byte, sigs map[string]string, err error) {
	text := string(data)
	idx := strings.Index(text, "\n"+signatureKey+" ")
	if idx < 0 {
		return nil, nil, fmt.Errorf("directory: consensus has no signatures")
	}

	body = []byte(text[:idx+1])
	sigs = map[string]string{}

	for _, line := range strings.Split(text[idx+1:], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rest, ok := strings.CutPrefix(line, signatureKey+" ")
		if !ok {
			// Unsigned content among the signature lines would be content the
			// authorities never approved.
			return nil, nil, fmt.Errorf("directory: unexpected line after the consensus body: %q", line)
		}
		fp, sig, ok := strings.Cut(strings.TrimSpace(rest), " ")
		if !ok || fp == "" || sig == "" {
			return nil, nil, fmt.Errorf("directory: malformed signature line")
		}
		if _, dup := sigs[fp]; dup {
			// Without this, one authority could contribute several signatures
			// and single-handedly satisfy a threshold meant to require many.
			return nil, nil, fmt.Errorf("directory: authority %s signed the consensus more than once", fp)
		}
		sigs[fp] = sig
	}
	if len(sigs) == 0 {
		return nil, nil, fmt.Errorf("directory: consensus has no signatures")
	}
	return body, sigs, nil
}

// AuthoritySet builds the map ParseConsensus expects from encoded keys.
func AuthoritySet(encoded []string) (map[string]ed25519.PublicKey, error) {
	out := make(map[string]ed25519.PublicKey, len(encoded))
	for _, s := range encoded {
		pub, err := DecodeKey(s)
		if err != nil {
			return nil, err
		}
		out[EncodeKey(pub)] = pub
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("directory: no authority keys configured")
	}
	return out, nil
}
