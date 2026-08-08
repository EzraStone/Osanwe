package directory

// This file contains the authority-side consensus workflow. Consensus parsing
// alone is not enough for an M-of-N directory: independent authorities need a
// way to construct exactly the same body, decide whether to sign it, and join
// their partial signatures without trusting the machine doing the joining.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// NewEpochConsensus constructs a canonical unsigned consensus for the epoch
// containing now. Epoch alignment is what lets independent authorities given
// the same descriptors produce identical signed bytes despite running a few
// seconds apart.
func NewEpochConsensus(relays []*Descriptor, now time.Time, epoch, lifetime time.Duration) (*Consensus, error) {
	if err := validateEpochSettings(epoch, lifetime); err != nil {
		return nil, err
	}
	validAfter := now.UTC().Truncate(epoch)
	c := &Consensus{
		ValidAfter: validAfter,
		ValidUntil: validAfter.Add(lifetime),
		Relays:     append([]*Descriptor(nil), relays...),
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	// Freeze the candidate bytes immediately. Sign will refuse if a caller
	// mutates the relay set or window between construction and signing.
	c.raw = c.body()
	return c, nil
}

// BuildConsensusPartial constructs and signs this epoch's canonical body. The
// result is a partial consensus: useful as input to CoSignConsensus or
// MergeConsensus, but acceptable to a client only when its configured
// threshold is one. This is a protocol primitive; a long-lived authority must
// also persist which body it signed for each epoch. The council command does.
func BuildConsensusPartial(relays []*Descriptor, id *Identity, now time.Time, epoch, lifetime time.Duration) ([]byte, error) {
	c, err := NewEpochConsensus(relays, now, epoch, lifetime)
	if err != nil {
		return nil, err
	}
	if err := c.Sign(id); err != nil {
		return nil, err
	}
	return c.Encode()
}

// ParseConsensusPartial validates a partial consensus without pretending that
// it has met a deployment's trust threshold. Every included signature must be
// mathematically valid for the public key named on its line. Trust in those
// keys is checked separately by the co-sign and merge operations.
func ParseConsensusPartial(data []byte, now time.Time) (*Consensus, error) {
	_, sigs, err := splitConsensusSignatures(data)
	if err != nil {
		return nil, err
	}
	authorities := make(map[string]ed25519.PublicKey, len(sigs))
	for fp := range sigs {
		pub, err := DecodeKey(fp)
		if err != nil {
			return nil, fmt.Errorf("directory: partial consensus names an invalid authority key %q: %w", fp, err)
		}
		if EncodeKey(pub) != fp {
			return nil, fmt.Errorf("directory: partial consensus authority key %q is not canonically encoded", fp)
		}
		authorities[fp] = pub
	}
	// Requiring all self-declared signers makes a malformed or forged partial
	// fail loudly instead of silently carrying garbage into the final file.
	return ParseConsensus(data, authorities, len(authorities), now)
}

// CoSignConsensus verifies and signs another authority's partial consensus.
// It signs only when the candidate:
//   - was signed by configured authorities;
//   - uses the configured epoch and lifetime; and
//   - is byte-for-byte the body this authority constructs from its own relay
//     descriptor directory.
//
// This last check is the substantive act of agreement. Merely checking that a
// candidate is well-formed would let its proposer choose the relay list alone.
func CoSignConsensus(data []byte, id *Identity, localRelays []*Descriptor, authorities map[string]ed25519.PublicKey, now time.Time, epoch, lifetime time.Duration) ([]byte, error) {
	c, err := PrepareConsensusCoSign(data, id, localRelays, authorities, now, epoch, lifetime)
	if err != nil {
		return nil, err
	}
	if err := c.Sign(id); err != nil {
		return nil, err
	}
	return c.Encode()
}

// PrepareConsensusCoSign performs all co-signing checks without using the
// private key. It lets a command acquire and update a persistent anti-
// equivocation record around the actual Sign call. Callers that do not need
// persistent state can use CoSignConsensus directly.
func PrepareConsensusCoSign(data []byte, id *Identity, localRelays []*Descriptor, authorities map[string]ed25519.PublicKey, now time.Time, epoch, lifetime time.Duration) (*Consensus, error) {
	if err := validateAuthoritySet(authorities, 1); err != nil {
		return nil, err
	}
	if err := authorityCanSign(id, authorities); err != nil {
		return nil, err
	}
	c, err := ParseConsensusPartial(data, now)
	if err != nil {
		return nil, fmt.Errorf("directory: refusing to co-sign invalid partial: %w", err)
	}
	if err := signaturesAreAuthorized(c.Signatures, authorities); err != nil {
		return nil, err
	}
	if _, already := c.Signatures[id.Fingerprint()]; already {
		return nil, fmt.Errorf("directory: authority %s has already signed this consensus", id.Fingerprint())
	}
	if err := ValidateConsensusEpoch(c, epoch, lifetime); err != nil {
		return nil, err
	}
	currentEpoch := now.UTC().Truncate(epoch)
	if !c.ValidAfter.Equal(currentEpoch) {
		return nil, fmt.Errorf("directory: refusing to co-sign non-current epoch %s; current epoch is %s",
			c.ValidAfter.UTC().Format(time.RFC3339), currentEpoch.Format(time.RFC3339))
	}

	expected := &Consensus{
		ValidAfter: c.ValidAfter,
		ValidUntil: c.ValidUntil,
		Relays:     append([]*Descriptor(nil), localRelays...),
	}
	if err := expected.validate(); err != nil {
		return nil, fmt.Errorf("directory: local relay set cannot form a consensus: %w", err)
	}
	if !bytes.Equal(expected.body(), c.Raw()) {
		return nil, fmt.Errorf("directory: refusing to co-sign body %s: it does not match the canonical body %s built from the local descriptor set (%s)",
			ConsensusBodyID(c.Raw()), ConsensusBodyID(expected.body()), relaySetDifference(c.Relays, localRelays))
	}

	return c, nil
}

// MergeConsensus combines signatures from partial consensus documents. It
// never merges bodies: every input must contain the same exact signed body.
// Only configured authorities are accepted, and the result must meet threshold
// before any output is returned.
func MergeConsensus(parts [][]byte, authorities map[string]ed25519.PublicKey, threshold int, now time.Time) ([]byte, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("directory: no partial consensus documents to merge")
	}
	if err := validateAuthoritySet(authorities, threshold); err != nil {
		return nil, err
	}

	var base *Consensus
	combined := make(map[string]string)
	for i, data := range parts {
		c, err := ParseConsensusPartial(data, now)
		if err != nil {
			return nil, fmt.Errorf("directory: partial %d is invalid: %w", i+1, err)
		}
		if err := signaturesAreAuthorized(c.Signatures, authorities); err != nil {
			return nil, fmt.Errorf("directory: partial %d: %w", i+1, err)
		}
		if base == nil {
			base = c
		} else if !bytes.Equal(base.Raw(), c.Raw()) {
			return nil, fmt.Errorf("directory: partial %d signs conflicting body %s; expected body %s",
				i+1, ConsensusBodyID(c.Raw()), ConsensusBodyID(base.Raw()))
		}
		for fp, sig := range c.Signatures {
			if previous, duplicate := combined[fp]; duplicate && previous != sig {
				return nil, fmt.Errorf("directory: authority %s supplied conflicting signatures for one consensus body", fp)
			}
			combined[fp] = sig
		}
	}

	if len(combined) < threshold {
		return nil, fmt.Errorf("directory: merged consensus carries %d distinct authorized signatures, need %d", len(combined), threshold)
	}
	base.Signatures = combined
	encoded, err := base.Encode()
	if err != nil {
		return nil, err
	}
	// Keep the threshold check in the same parser clients use. This makes the
	// aggregator incapable of emitting something it merely believes clients
	// will accept.
	if _, err := ParseConsensus(encoded, authorities, threshold, now); err != nil {
		return nil, fmt.Errorf("directory: merged consensus failed final verification: %w", err)
	}
	return encoded, nil
}

// ValidateConsensusEpoch verifies the deterministic time parameters an
// authority agreed to use. A fresh body from a differently configured epoch is
// still a conflict and must not be co-signed accidentally.
func ValidateConsensusEpoch(c *Consensus, epoch, lifetime time.Duration) error {
	if c == nil {
		return fmt.Errorf("directory: nil consensus")
	}
	if err := validateEpochSettings(epoch, lifetime); err != nil {
		return err
	}
	if !c.ValidAfter.Equal(c.ValidAfter.UTC().Truncate(epoch)) {
		return fmt.Errorf("directory: consensus valid-after %s is not aligned to the %s epoch",
			c.ValidAfter.UTC().Format(time.RFC3339), epoch)
	}
	wantUntil := c.ValidAfter.Add(lifetime)
	if !c.ValidUntil.Equal(wantUntil) {
		return fmt.Errorf("directory: consensus lifetime is %s; configured lifetime is %s",
			c.ValidUntil.Sub(c.ValidAfter), lifetime)
	}
	return nil
}

// ConsensusBodyID is a non-secret SHA-256 identifier for the exact signed body.
// Operators can compare it out of band without pasting an entire consensus.
func ConsensusBodyID(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validateEpochSettings(epoch, lifetime time.Duration) error {
	if epoch < time.Second || epoch%time.Second != 0 {
		return fmt.Errorf("directory: consensus epoch must be a whole number of seconds and at least one second")
	}
	if lifetime <= epoch {
		return fmt.Errorf("directory: consensus lifetime (%s) must be longer than its epoch (%s)", lifetime, epoch)
	}
	if lifetime%time.Second != 0 {
		return fmt.Errorf("directory: consensus lifetime must be a whole number of seconds")
	}
	return nil
}

func validateAuthoritySet(authorities map[string]ed25519.PublicKey, threshold int) error {
	if threshold < 1 {
		return fmt.Errorf("directory: threshold must be at least 1")
	}
	if len(authorities) < threshold {
		return fmt.Errorf("directory: %d authorities configured but threshold is %d; no consensus could ever satisfy it", len(authorities), threshold)
	}
	for fp, pub := range authorities {
		if EncodeKey(pub) != fp {
			return fmt.Errorf("directory: authority map key %q does not match its public key", fp)
		}
	}
	return nil
}

func authorityCanSign(id *Identity, authorities map[string]ed25519.PublicKey) error {
	if id == nil || len(id.Public) != ed25519.PublicKeySize || len(id.Private) != ed25519.PrivateKeySize {
		return fmt.Errorf("directory: cannot co-sign without a valid authority identity")
	}
	trusted, ok := authorities[id.Fingerprint()]
	if !ok || !trusted.Equal(id.Public) {
		return fmt.Errorf("directory: this authority %s is not in the configured authority set", id.Fingerprint())
	}
	return nil
}

func signaturesAreAuthorized(signatures map[string]string, authorities map[string]ed25519.PublicKey) error {
	for fp := range signatures {
		if _, ok := authorities[fp]; !ok {
			return fmt.Errorf("directory: partial consensus was signed by unconfigured authority %s", fp)
		}
	}
	return nil
}

func relaySetDifference(candidate, local []*Descriptor) string {
	type relayDocument struct {
		name    string
		encoded []byte
	}
	candidateByID := make(map[string]relayDocument, len(candidate))
	localByID := make(map[string]relayDocument, len(local))
	for _, d := range candidate {
		candidateByID[d.Identity] = relayDocument{name: d.Nickname, encoded: d.Encoded()}
	}
	for _, d := range local {
		localByID[d.Identity] = relayDocument{name: d.Nickname, encoded: d.Encoded()}
	}
	var differences []string
	for identity, proposed := range candidateByID {
		approved, ok := localByID[identity]
		switch {
		case !ok:
			differences = append(differences, fmt.Sprintf("candidate-only %s/%s", proposed.name, identity))
		case !bytes.Equal(proposed.encoded, approved.encoded):
			differences = append(differences, fmt.Sprintf("changed %s/%s", proposed.name, identity))
		}
	}
	for identity, approved := range localByID {
		if _, ok := candidateByID[identity]; !ok {
			differences = append(differences, fmt.Sprintf("local-only %s/%s", approved.name, identity))
		}
	}
	sort.Strings(differences)
	if len(differences) == 0 {
		return "different exact descriptor bytes"
	}
	if len(differences) > 8 {
		differences = append(differences[:8], fmt.Sprintf("and %d more", len(differences)-8))
	}
	return strings.Join(differences, ", ")
}
