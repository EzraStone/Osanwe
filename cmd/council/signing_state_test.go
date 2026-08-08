package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
)

func TestConsensusSigningStatePreventsEquivocationAcrossRuns(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "authority.consensus-state")
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	epoch := 30 * time.Minute
	now := time.Now().UTC().Truncate(epoch).Add(time.Minute)
	alpha := councilTestRelay(t, "alpha", now)
	beta := councilTestRelay(t, "beta", now)

	proposal := func(at time.Time, relays ...*directory.Descriptor) *directory.Consensus {
		t.Helper()
		c, err := directory.NewEpochConsensus(relays, at, epoch, 3*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	first := proposal(now, alpha)
	if _, err := signConsensusWithState(statePath, id, first); err != nil {
		t.Fatalf("first sign: %v", err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state mode = %o, want 600", info.Mode().Perm())
	}

	// Retrying the same exact body is safe and necessary after an output error.
	if _, err := signConsensusWithState(statePath, id, proposal(now, alpha)); err != nil {
		t.Fatalf("idempotent same-body sign: %v", err)
	}
	if _, err := signConsensusWithState(statePath, id, proposal(now, beta)); err == nil {
		t.Fatal("state allowed the authority to sign a conflicting body in one epoch")
	} else if !strings.Contains(err.Error(), "already signed body") {
		t.Fatalf("conflict error = %v", err)
	}

	if _, err := signConsensusWithState(statePath, id, proposal(now.Add(epoch), beta)); err != nil {
		t.Fatalf("next epoch sign: %v", err)
	}
	if _, err := signConsensusWithState(statePath, id, proposal(now, alpha)); err == nil {
		t.Fatal("state allowed a signing rollback to the previous epoch")
	} else if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback error = %v", err)
	}
}

func TestConsensusSigningStateSerializesConcurrentSigners(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state")
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	c, err := directory.NewEpochConsensus(
		[]*directory.Descriptor{councilTestRelay(t, "alpha", time.Now())},
		time.Now(), 30*time.Minute, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	first, err := beginConsensusSigning(statePath, id, c)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := beginConsensusSigning(statePath, id, c); err == nil {
		t.Fatal("a second signer acquired the same identity state concurrently")
	} else if !strings.Contains(err.Error(), "another signer may be running") {
		t.Fatalf("lock error = %v", err)
	}
}

func TestConsensusSigningStateFailsClosedOnCorruptionOrWrongIdentity(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state")
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	other, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	c, err := directory.NewEpochConsensus(
		[]*directory.Descriptor{councilTestRelay(t, "alpha", time.Now())},
		time.Now(), 30*time.Minute, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := beginConsensusSigning(statePath, id, c); err == nil {
		t.Fatal("corrupt signing state was ignored")
	}
	if _, err := os.Stat(statePath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("failed begin left a lock behind: %v", err)
	}

	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := signConsensusWithState(statePath, id, c); err != nil {
		t.Fatal(err)
	}
	if _, err := beginConsensusSigning(statePath, other, c); err == nil {
		t.Fatal("another authority reused the first authority's signing state")
	} else if !strings.Contains(err.Error(), "belongs to") {
		t.Fatalf("identity mismatch error = %v", err)
	}
}

func TestOversizedConsensusDoesNotCommitSigningState(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state")
	authority, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	relayID, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d := &directory.Descriptor{
		Nickname:     "oversized",
		Address:      "oversized.example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     relayID.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Contact:      strings.Repeat("x", directory.MaxConsensusSize),
		Published:    now.Add(-time.Hour),
		Expires:      now.Add(24 * time.Hour),
	}
	if _, err := d.Sign(relayID); err != nil {
		t.Fatal(err)
	}
	c, err := directory.NewEpochConsensus([]*directory.Descriptor{d}, now, 30*time.Minute, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signConsensusWithState(statePath, authority, c); err == nil {
		t.Fatal("oversized consensus was signed and recorded")
	} else if !strings.Contains(err.Error(), "protocol limit") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("oversized body committed signing state: %v", err)
	}
}
