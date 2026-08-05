package directory

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

// relay builds a signed descriptor for a fresh relay identity.
func relay(t *testing.T, nickname string, dests ...string) *Descriptor {
	t.Helper()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if len(dests) == 0 {
		dests = []string{"api.anthropic.com:443"}
	}
	now := time.Now()
	d := &Descriptor{
		Nickname:     nickname,
		Address:      nickname + ".example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     id.Fingerprint(),
		Destinations: dests,
		Published:    now,
		Expires:      now.Add(24 * time.Hour),
	}
	if _, err := d.Sign(id); err != nil {
		t.Fatalf("descriptor Sign: %v", err)
	}
	return d
}

// authorities builds n authority identities and the trust map for them.
func authorities(t *testing.T, n int) ([]*Identity, map[string]ed25519.PublicKey) {
	t.Helper()
	var ids []*Identity
	var encoded []string
	for i := 0; i < n; i++ {
		id, err := GenerateIdentity()
		if err != nil {
			t.Fatalf("GenerateIdentity: %v", err)
		}
		ids = append(ids, id)
		encoded = append(encoded, id.Fingerprint())
	}
	set, err := AuthoritySet(encoded)
	if err != nil {
		t.Fatalf("AuthoritySet: %v", err)
	}
	return ids, set
}

func buildConsensus(t *testing.T, signers []*Identity, relays ...*Descriptor) []byte {
	t.Helper()
	now := time.Now()
	c := &Consensus{
		ValidAfter: now.Add(-time.Minute),
		ValidUntil: now.Add(time.Hour),
		Relays:     relays,
	}
	for _, id := range signers {
		if err := c.Sign(id); err != nil {
			t.Fatalf("consensus Sign: %v", err)
		}
	}
	encoded, err := c.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return encoded
}

func TestConsensusRoundTrip(t *testing.T) {
	ids, set := authorities(t, 3)
	r1, r2 := relay(t, "alpha"), relay(t, "beta")

	data := buildConsensus(t, ids, r1, r2)

	c, err := ParseConsensus(data, set, 2, time.Now())
	if err != nil {
		t.Fatalf("ParseConsensus: %v", err)
	}
	if len(c.Relays) != 2 {
		t.Fatalf("got %d relays, want 2", len(c.Relays))
	}
	// Sorted by nickname in the body, so order is deterministic.
	if c.Relays[0].Nickname != "alpha" || c.Relays[1].Nickname != "beta" {
		t.Errorf("relays = %q, %q", c.Relays[0].Nickname, c.Relays[1].Nickname)
	}
	if c.Relays[0].Address != "alpha.example:8443" {
		t.Errorf("address = %q", c.Relays[0].Address)
	}
}

// TestThresholdIsEnforced is the load-bearing test: too few signatures must
// fail no matter how well-formed the document is.
func TestThresholdIsEnforced(t *testing.T) {
	ids, set := authorities(t, 3)
	r := relay(t, "alpha")

	oneSig := buildConsensus(t, ids[:1], r)

	if _, err := ParseConsensus(oneSig, set, 1, time.Now()); err != nil {
		t.Fatalf("one signature rejected at threshold 1: %v", err)
	}
	if _, err := ParseConsensus(oneSig, set, 2, time.Now()); err == nil {
		t.Fatal("one signature accepted at threshold 2; a single compromised authority could direct every client")
	}
	if _, err := ParseConsensus(oneSig, set, 3, time.Now()); err == nil {
		t.Fatal("one signature accepted at threshold 3")
	}

	twoSigs := buildConsensus(t, ids[:2], r)
	if _, err := ParseConsensus(twoSigs, set, 2, time.Now()); err != nil {
		t.Errorf("two signatures rejected at threshold 2: %v", err)
	}
	if _, err := ParseConsensus(twoSigs, set, 3, time.Now()); err == nil {
		t.Error("two signatures accepted at threshold 3")
	}
}

func TestSignaturesFromUnknownAuthoritiesDoNotCount(t *testing.T) {
	known, set := authorities(t, 2)
	stranger, _ := GenerateIdentity()
	r := relay(t, "alpha")

	// One known signature plus one from a key nobody trusts.
	data := buildConsensus(t, []*Identity{known[0], stranger}, r)

	if _, err := ParseConsensus(data, set, 2, time.Now()); err == nil {
		t.Fatal("a signature from an unknown authority counted toward the threshold")
	}
	// But it must not break parsing either: adding an authority to the network
	// should not break clients that have not heard of it yet.
	if _, err := ParseConsensus(data, set, 1, time.Now()); err != nil {
		t.Errorf("an unknown authority's signature broke an otherwise valid consensus: %v", err)
	}
}

func TestOneAuthorityCannotSignTwice(t *testing.T) {
	ids, set := authorities(t, 3)
	r := relay(t, "alpha")
	data := buildConsensus(t, ids[:1], r)

	// Duplicate the single signature line, as an attacker would to inflate the
	// count past a threshold meant to require several distinct authorities.
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	sigLine := lines[len(lines)-1]
	doubled := append(append([]byte(nil), data...), append(sigLine, '\n')...)

	if _, err := ParseConsensus(doubled, set, 2, time.Now()); err == nil {
		t.Fatal("one authority satisfied a threshold of 2 by signing twice")
	}
}

func TestStaleConsensusIsRejected(t *testing.T) {
	ids, set := authorities(t, 2)
	r := relay(t, "alpha")

	now := time.Now()
	c := &Consensus{
		ValidAfter: now.Add(-4 * time.Hour),
		ValidUntil: now.Add(-2 * time.Hour), // already expired
		Relays:     []*Descriptor{r},
	}
	for _, id := range ids {
		if err := c.Sign(id); err != nil {
			t.Fatalf("Sign: %v", err)
		}
	}
	data, err := c.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Signatures are perfectly valid; only freshness fails. That is exactly
	// what a replayed consensus looks like.
	if _, err := ParseConsensus(data, set, 2, now); err == nil {
		t.Fatal("expired consensus accepted; an old copy could be replayed after a relay was withdrawn")
	} else if !strings.Contains(err.Error(), "fresh") {
		t.Errorf("error = %v, want it to name freshness", err)
	}

	// Inside its window it must verify, proving the rejection was about time.
	if _, err := ParseConsensus(data, set, 2, now.Add(-3*time.Hour)); err != nil {
		t.Errorf("consensus rejected inside its own validity window: %v", err)
	}
}

func TestNotYetValidConsensusIsRejected(t *testing.T) {
	ids, set := authorities(t, 2)
	now := time.Now()
	c := &Consensus{
		ValidAfter: now.Add(2 * time.Hour),
		ValidUntil: now.Add(4 * time.Hour),
		Relays:     []*Descriptor{relay(t, "alpha")},
	}
	for _, id := range ids {
		_ = c.Sign(id)
	}
	data, _ := c.Encode()

	if _, err := ParseConsensus(data, set, 2, now); err == nil {
		t.Fatal("accepted a consensus that is not valid yet")
	}
}

func TestBodyMutationsAreRejected(t *testing.T) {
	ids, set := authorities(t, 2)
	r := relay(t, "alpha")
	data := buildConsensus(t, ids, r)

	mutations := map[string][]byte{
		"changed valid-until": bytes.Replace(data, []byte("valid-until"), []byte("valid-UNTIL"), 1),
		"dropped a relay":     dropRelayLine(data),
		"corrupted relay":     bytes.Replace(data, []byte("relay "), []byte("relay X"), 1),
	}
	for name, mutated := range mutations {
		if bytes.Equal(mutated, data) {
			t.Fatalf("%s: mutation was a no-op", name)
		}
		if _, err := ParseConsensus(mutated, set, 2, time.Now()); err == nil {
			t.Errorf("%s: mutated consensus accepted", name)
		}
	}
}

func dropRelayLine(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	var out [][]byte
	for _, l := range lines {
		if bytes.HasPrefix(l, []byte("relay ")) {
			continue
		}
		out = append(out, l)
	}
	return bytes.Join(out, []byte("\n"))
}

// TestForgedRelayEntryIsRejected checks the property that survives even a
// fully compromised authority: it cannot invent a relay, because it cannot
// forge that relay's own signature.
func TestForgedRelayEntryIsRejected(t *testing.T) {
	ids, set := authorities(t, 2)
	honest := relay(t, "alpha")

	// A hostile authority takes the honest relay's descriptor and rewrites the
	// address to point at itself, then signs the consensus normally.
	forged := *honest
	forged.Address = "attacker.example:8443"
	tampered := bytes.Replace(honest.Raw(), []byte("alpha.example:8443"), []byte("attacker.example:8443"), 1)
	forged.raw = tampered

	data := buildConsensus(t, ids, &forged)

	if _, err := ParseConsensus(data, set, 2, time.Now()); err == nil {
		t.Fatal("a consensus signed by every authority smuggled in a relay descriptor the relay never signed")
	}
}

func TestSignRefusesToChangeADocumentMidway(t *testing.T) {
	ids, _ := authorities(t, 2)
	now := time.Now()
	c := &Consensus{
		ValidAfter: now,
		ValidUntil: now.Add(time.Hour),
		Relays:     []*Descriptor{relay(t, "alpha")},
	}
	if err := c.Sign(ids[0]); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Adding a relay after the first signature would silently invalidate it.
	c.Relays = append(c.Relays, relay(t, "beta"))
	if err := c.Sign(ids[1]); err == nil {
		t.Fatal("signed a consensus that changed after an earlier signature was applied")
	}
}

func TestParseRejectsImpossibleConfiguration(t *testing.T) {
	_, set := authorities(t, 2) // trust map with two authorities in it
	ids, _ := authorities(t, 3)
	good := buildConsensus(t, ids, relay(t, "alpha"))

	if _, err := ParseConsensus(good, set, 0, time.Now()); err == nil {
		t.Error("accepted a threshold of 0")
	}
	if _, err := ParseConsensus(good, set, 5, time.Now()); err == nil {
		t.Error("accepted a threshold higher than the number of configured authorities")
	}
}

func TestUsableFiltersByFreshnessAndDestination(t *testing.T) {
	ids, set := authorities(t, 1)
	anthropic := relay(t, "alpha", "api.anthropic.com:443")
	openai := relay(t, "beta", "api.openai.com:443")

	data := buildConsensus(t, ids, anthropic, openai)
	c, err := ParseConsensus(data, set, 1, time.Now())
	if err != nil {
		t.Fatalf("ParseConsensus: %v", err)
	}

	now := time.Now()
	if got := c.Usable(now, "api.anthropic.com:443"); len(got) != 1 || got[0].Nickname != "alpha" {
		t.Errorf("Usable for anthropic = %v", names(got))
	}
	if got := c.Usable(now, "api.openai.com:443"); len(got) != 1 || got[0].Nickname != "beta" {
		t.Errorf("Usable for openai = %v", names(got))
	}
	if got := c.Usable(now, "nobody.example:443"); len(got) != 0 {
		t.Errorf("Usable for an unserved destination = %v", names(got))
	}
	if got := c.Usable(now, ""); len(got) != 2 {
		t.Errorf("Usable with no destination filter = %v, want both", names(got))
	}
	// Descriptors expire independently of the consensus that carries them.
	if got := c.Usable(now.Add(48*time.Hour), ""); len(got) != 0 {
		t.Errorf("expired descriptors still reported usable: %v", names(got))
	}
}

func names(ds []*Descriptor) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Nickname
	}
	return out
}

func TestEncodeRefusesUnsignedConsensus(t *testing.T) {
	c := &Consensus{
		ValidAfter: time.Now(),
		ValidUntil: time.Now().Add(time.Hour),
		Relays:     []*Descriptor{relay(t, "alpha")},
	}
	if _, err := c.Encode(); err == nil {
		t.Fatal("encoded a consensus with no signatures")
	}
}
