package directory

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestEpochConsensusIsDeterministic(t *testing.T) {
	ids, _ := authorities(t, 2)
	alpha, beta := relay(t, "alpha"), relay(t, "beta")
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(2 * time.Minute)

	first, err := BuildConsensusPartial([]*Descriptor{beta, alpha}, ids[0], now, epoch, lifetime)
	if err != nil {
		t.Fatalf("BuildConsensusPartial(first): %v", err)
	}
	second, err := BuildConsensusPartial([]*Descriptor{alpha, beta}, ids[1], now.Add(19*time.Minute), epoch, lifetime)
	if err != nil {
		t.Fatalf("BuildConsensusPartial(second): %v", err)
	}

	a, err := ParseConsensusPartial(first, now)
	if err != nil {
		t.Fatalf("ParseConsensusPartial(first): %v", err)
	}
	b, err := ParseConsensusPartial(second, now)
	if err != nil {
		t.Fatalf("ParseConsensusPartial(second): %v", err)
	}
	if !bytes.Equal(a.Raw(), b.Raw()) {
		t.Fatalf("authorities built different bodies in one epoch: %s != %s",
			ConsensusBodyID(a.Raw()), ConsensusBodyID(b.Raw()))
	}
	if !a.ValidAfter.Equal(now.Truncate(epoch)) {
		t.Errorf("valid-after = %s, want epoch start %s", a.ValidAfter, now.Truncate(epoch))
	}

	next, err := BuildConsensusPartial([]*Descriptor{alpha, beta}, ids[0], now.Add(epoch), epoch, lifetime)
	if err != nil {
		t.Fatalf("BuildConsensusPartial(next): %v", err)
	}
	c, err := ParseConsensusPartial(next, now.Add(epoch))
	if err != nil {
		t.Fatalf("ParseConsensusPartial(next): %v", err)
	}
	if bytes.Equal(a.Raw(), c.Raw()) {
		t.Fatal("adjacent epochs produced the same signed body")
	}
}

func TestEpochProposalIsFrozenBeforeFirstSignature(t *testing.T) {
	id, _ := GenerateIdentity()
	now := time.Now()
	c, err := NewEpochConsensus([]*Descriptor{relay(t, "alpha")}, now, 30*time.Minute, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.Relays = append(c.Relays, relay(t, "beta"))
	if err := c.Sign(id); err == nil {
		t.Fatal("signed a proposal mutated after its canonical body was constructed")
	} else if !strings.Contains(err.Error(), "changed") {
		t.Fatalf("error = %v", err)
	}
}

func TestConsensusLibraryRejectsRelayExpiringInsideItsWindow(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(time.Minute)
	validAfter := now.Truncate(epoch)
	d := &Descriptor{
		Nickname:     "short-lived",
		Address:      "short-lived.example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     id.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Published:    validAfter.Add(-time.Hour),
		Expires:      validAfter.Add(lifetime - time.Second),
	}
	if _, err := d.Sign(id); err != nil {
		t.Fatalf("Sign descriptor: %v", err)
	}
	if _, err := NewEpochConsensus([]*Descriptor{d}, now, epoch, lifetime); err == nil {
		t.Fatal("NewEpochConsensus signed a relay that expires inside its validity window")
	} else if !strings.Contains(err.Error(), "throughout") {
		t.Fatalf("error = %v, want full-window validity refusal", err)
	}
}

func TestCoSignRequiresLocalAgreement(t *testing.T) {
	ids, set := authorities(t, 3)
	alpha, beta := relay(t, "alpha"), relay(t, "beta")
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(time.Minute)

	partial, err := BuildConsensusPartial([]*Descriptor{alpha, beta}, ids[0], now, epoch, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	coSigned, err := CoSignConsensus(partial, ids[1], []*Descriptor{beta, alpha}, set, now, epoch, lifetime)
	if err != nil {
		t.Fatalf("CoSignConsensus: %v", err)
	}
	c, err := ParseConsensus(coSigned, set, 2, now)
	if err != nil {
		t.Fatalf("two-signature result was not client-valid: %v", err)
	}
	if len(c.Signatures) != 2 {
		t.Fatalf("signatures = %d, want 2", len(c.Signatures))
	}

	if _, err := CoSignConsensus(partial, ids[1], []*Descriptor{alpha}, set, now, epoch, lifetime); err == nil {
		t.Fatal("co-signed a candidate that omitted a locally approved relay")
	} else if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("mismatch error = %v", err)
	}
	if _, err := CoSignConsensus(coSigned, ids[1], []*Descriptor{alpha, beta}, set, now, epoch, lifetime); err == nil {
		t.Fatal("the same authority co-signed twice")
	}
}

func TestCoSignRejectsUnconfiguredProposer(t *testing.T) {
	ids, set := authorities(t, 2)
	stranger, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r := relay(t, "alpha")
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(time.Minute)
	partial, err := BuildConsensusPartial([]*Descriptor{r}, stranger, now, epoch, lifetime)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := CoSignConsensus(partial, ids[0], []*Descriptor{r}, set, now, epoch, lifetime); err == nil {
		t.Fatal("co-signed a proposal from an unconfigured authority")
	} else if !strings.Contains(err.Error(), "unconfigured authority") {
		t.Errorf("error = %v", err)
	}
}

func TestCoSignRejectsWrongEpochConfiguration(t *testing.T) {
	ids, set := authorities(t, 2)
	r := relay(t, "alpha")
	now := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)
	misaligned := &Consensus{
		ValidAfter: now.UTC().Truncate(time.Hour).Add(time.Minute),
		ValidUntil: now.UTC().Truncate(time.Hour).Add(time.Minute).Add(4 * time.Hour),
		Relays:     []*Descriptor{r},
	}
	if err := misaligned.Sign(ids[0]); err != nil {
		t.Fatal(err)
	}
	partial, err := misaligned.Encode()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := CoSignConsensus(partial, ids[1], []*Descriptor{r}, set, now, time.Hour, 4*time.Hour); err == nil {
		t.Fatal("co-signed a body whose valid-after was not epoch-aligned")
	}
	if _, err := CoSignConsensus(partial, ids[1], []*Descriptor{r}, set, now, time.Hour, 3*time.Hour); err == nil {
		t.Fatal("co-signed a body built for a different lifetime")
	}
}

func TestCoSignRejectsPriorEpochEvenWhileFresh(t *testing.T) {
	ids, set := authorities(t, 2)
	r := relay(t, "alpha")
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(10 * time.Minute)
	partial, err := BuildConsensusPartial([]*Descriptor{r}, ids[0], now.Add(-epoch), epoch, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	// The preceding epoch remains inside its three-hour validity window. It is
	// safe to serve for availability, but adding a new signature now would
	// endorse a stale view after the signing round has moved on.
	if _, err := ParseConsensusPartial(partial, now); err != nil {
		t.Fatalf("test setup: prior epoch should still be fresh: %v", err)
	}
	if _, err := CoSignConsensus(partial, ids[1], []*Descriptor{r}, set, now, epoch, lifetime); err == nil {
		t.Fatal("co-signed a prior epoch that happened to remain fresh")
	} else if !strings.Contains(err.Error(), "non-current epoch") {
		t.Errorf("error = %v", err)
	}
}

func TestMergeConsensusCombinesOnlyMatchingAuthorizedBodies(t *testing.T) {
	ids, set := authorities(t, 3)
	alpha, beta := relay(t, "alpha"), relay(t, "beta")
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(time.Minute)

	parts := make([][]byte, 2)
	for i := range parts {
		var err error
		parts[i], err = BuildConsensusPartial([]*Descriptor{alpha, beta}, ids[i], now, epoch, lifetime)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
	merged, err := MergeConsensus(parts, set, 2, now)
	if err != nil {
		t.Fatalf("MergeConsensus: %v", err)
	}
	if _, err := ParseConsensus(merged, set, 2, now); err != nil {
		t.Fatalf("merged document was not client-valid: %v", err)
	}
	if _, err := MergeConsensus(parts, set, 3, now); err == nil {
		t.Fatal("two partials met a threshold of three")
	}

	conflict, err := BuildConsensusPartial([]*Descriptor{alpha}, ids[2], now, epoch, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MergeConsensus([][]byte{parts[0], conflict}, set, 2, now); err == nil {
		t.Fatal("merged signatures over conflicting relay lists")
	} else if !strings.Contains(err.Error(), "conflicting body") {
		t.Errorf("conflict error = %v", err)
	}
}

func TestMergeRejectsInvalidAndUnknownSignatures(t *testing.T) {
	ids, set := authorities(t, 2)
	stranger, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r := relay(t, "alpha")
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(time.Minute)
	known, _ := BuildConsensusPartial([]*Descriptor{r}, ids[0], now, epoch, lifetime)
	unknown, _ := BuildConsensusPartial([]*Descriptor{r}, stranger, now, epoch, lifetime)

	if _, err := MergeConsensus([][]byte{known, unknown}, set, 2, now); err == nil {
		t.Fatal("merged a partial from an unconfigured authority")
	}

	broken := append([]byte(nil), known...)
	last := bytes.LastIndex(broken, []byte("signature "))
	broken[len(broken)-2] ^= 1 // mutate base64 while leaving the body untouched
	if last < 0 {
		t.Fatal("test partial had no signature")
	}
	if _, err := ParseConsensusPartial(broken, now); err == nil {
		t.Fatal("accepted a forged partial signature")
	}
}

func TestConsensusRejectsDuplicateRelayIdentity(t *testing.T) {
	id, _ := GenerateIdentity()
	r := relay(t, "alpha")
	now := time.Now()
	c := &Consensus{
		ValidAfter: now.Add(-time.Minute),
		ValidUntil: now.Add(time.Hour),
		Relays:     []*Descriptor{r, r},
	}
	if err := c.Sign(id); err == nil {
		t.Fatal("signed a consensus that weighted one relay identity twice")
	}
}

func TestEpochSettingsMustFitWirePrecision(t *testing.T) {
	id, _ := GenerateIdentity()
	r := relay(t, "alpha")
	tests := []struct {
		epoch    time.Duration
		lifetime time.Duration
	}{
		{500 * time.Millisecond, time.Hour},
		{time.Minute + time.Nanosecond, time.Hour},
		{time.Hour, time.Hour},
		{time.Minute, time.Hour + time.Nanosecond},
	}
	for _, tc := range tests {
		if _, err := BuildConsensusPartial([]*Descriptor{r}, id, time.Now(), tc.epoch, tc.lifetime); err == nil {
			t.Errorf("accepted epoch=%s lifetime=%s", tc.epoch, tc.lifetime)
		}
	}
}
