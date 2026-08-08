package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
)

const signingStateVersion = 1

type signingState struct {
	Version    int    `json:"version"`
	Authority  string `json:"authority"`
	ValidAfter string `json:"valid_after"`
	BodySHA256 string `json:"body_sha256"`
}

// consensusSigningGuard serializes signers using one identity and remembers
// the last epoch/body that identity signed. Without persistent vote locking, a
// restart or two concurrent commands could make an honest authority sign two
// conflicting bodies for one epoch, allowing both sides of a split vote to
// reach threshold.
type consensusSigningGuard struct {
	path      string
	lockPath  string
	authority string
	epoch     time.Time
	bodyID    string
	advance   bool
	closed    bool
}

func beginConsensusSigning(path string, id *directory.Identity, c *directory.Consensus) (*consensusSigningGuard, error) {
	if path == "" {
		return nil, fmt.Errorf("council: consensus signing-state path is empty")
	}
	if id == nil || id.Fingerprint() == "" {
		return nil, fmt.Errorf("council: cannot lock consensus signing without an authority identity")
	}
	if c == nil || c.ValidAfter.IsZero() || len(c.Raw()) == 0 {
		return nil, fmt.Errorf("council: cannot lock an incomplete consensus proposal")
	}

	g := &consensusSigningGuard{
		path:      path,
		lockPath:  path + ".lock",
		authority: id.Fingerprint(),
		epoch:     c.ValidAfter.UTC(),
		bodyID:    directory.ConsensusBodyID(c.Raw()),
		advance:   true,
	}
	// A directory is used as the lock because mkdir is atomic and available in
	// the standard library. A process that crashes while signing leaves a stale
	// lock and fails closed; the operator must inspect it before removing it.
	if err := os.Mkdir(g.lockPath, 0o700); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("council: consensus signing lock %s exists; another signer may be running (if it crashed, inspect state before removing the lock)", g.lockPath)
		}
		return nil, fmt.Errorf("council: creating consensus signing lock %s: %w", g.lockPath, err)
	}

	state, exists, err := readSigningState(path)
	if err != nil {
		g.Close()
		return nil, err
	}
	if !exists {
		return g, nil
	}
	if state.Authority != g.authority {
		g.Close()
		return nil, fmt.Errorf("council: signing state %s belongs to %s, not this authority %s",
			path, state.Authority, g.authority)
	}
	previousEpoch, err := time.Parse(time.RFC3339, state.ValidAfter)
	if err != nil || previousEpoch.UTC().Format(time.RFC3339) != state.ValidAfter {
		g.Close()
		return nil, fmt.Errorf("council: signing state %s has invalid valid_after %q", path, state.ValidAfter)
	}
	switch {
	case previousEpoch.After(g.epoch):
		g.Close()
		return nil, fmt.Errorf("council: refusing signing-state rollback from epoch %s to %s",
			previousEpoch.UTC().Format(time.RFC3339), g.epoch.Format(time.RFC3339))
	case previousEpoch.Equal(g.epoch) && state.BodySHA256 != g.bodyID:
		g.Close()
		return nil, fmt.Errorf("council: authority %s already signed body %s for epoch %s; refusing conflicting body %s",
			g.authority, state.BodySHA256, g.epoch.Format(time.RFC3339), g.bodyID)
	case previousEpoch.Equal(g.epoch):
		// Signing the identical bytes is idempotent. Ed25519 produces the same
		// signature, so retrying after an output-write failure is safe.
		g.advance = false
	}
	return g, nil
}

func signConsensusWithState(path string, id *directory.Identity, c *directory.Consensus) ([]byte, error) {
	guard, err := beginConsensusSigning(path, id, c)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	if err := c.Sign(id); err != nil {
		return nil, err
	}
	encoded, err := c.Encode()
	if err != nil {
		return nil, err
	}
	if err := guard.Commit(); err != nil {
		return nil, err
	}
	return encoded, nil
}

func signingStatePath(configured, identityPath string) string {
	if configured != "" {
		return configured
	}
	return identityPath + ".consensus-state"
}

// Commit records the vote before the signed document is published. If a crash
// happens after this write but before output, retrying the identical body is
// allowed; a different body remains locked out.
func (g *consensusSigningGuard) Commit() error {
	if g == nil || g.closed {
		return fmt.Errorf("council: consensus signing guard is not active")
	}
	if !g.advance {
		return nil
	}
	state := signingState{
		Version:    signingStateVersion,
		Authority:  g.authority,
		ValidAfter: g.epoch.UTC().Format(time.RFC3339),
		BodySHA256: g.bodyID,
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("council: encoding consensus signing state: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeAtomic(g.path, encoded, 0o600); err != nil {
		return fmt.Errorf("council: recording consensus signing state: %w", err)
	}
	g.advance = false
	return nil
}

func (g *consensusSigningGuard) Close() {
	if g == nil || g.closed {
		return
	}
	g.closed = true
	_ = os.Remove(g.lockPath)
}

func readSigningState(path string) (signingState, bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return signingState{}, false, nil
	}
	if err != nil {
		return signingState{}, false, fmt.Errorf("council: reading consensus signing state %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return signingState{}, false, fmt.Errorf("council: reading consensus signing state %s: %w", path, err)
	}
	if len(data) > 4096 {
		return signingState{}, false, fmt.Errorf("council: consensus signing state %s is unexpectedly large", path)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var state signingState
	if err := dec.Decode(&state); err != nil {
		return signingState{}, false, fmt.Errorf("council: parsing consensus signing state %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return signingState{}, false, fmt.Errorf("council: signing state %s has trailing content", path)
	}
	if state.Version != signingStateVersion {
		return signingState{}, false, fmt.Errorf("council: signing state %s has version %d, want %d", path, state.Version, signingStateVersion)
	}
	if state.Authority == "" {
		return signingState{}, false, fmt.Errorf("council: signing state %s has no authority", path)
	}
	digest, err := hex.DecodeString(state.BodySHA256)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != state.BodySHA256 {
		return signingState{}, false, fmt.Errorf("council: signing state %s has invalid body_sha256 %q", path, state.BodySHA256)
	}
	canonical, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return signingState{}, false, fmt.Errorf("council: encoding consensus signing state: %w", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) {
		return signingState{}, false, fmt.Errorf("council: signing state %s is not canonically encoded", path)
	}
	return state, true, nil
}
