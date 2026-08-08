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

const servingStateVersion = 1

type servingState struct {
	Version    int    `json:"version"`
	ValidAfter string `json:"valid_after"`
	BodySHA256 string `json:"body_sha256"`
}

func servingStatePath(configured, consensusPath string) string {
	if configured != "" {
		return configured
	}
	return consensusPath + ".serve-state"
}

// recordServedConsensus persists the highest epoch/body this mirror has
// installed. Client-visible rollback protection must survive a process restart;
// an in-memory comparison alone would forget the most important prior fact.
func recordServedConsensus(path string, c *directory.Consensus) error {
	if path == "" {
		return fmt.Errorf("council: consensus serving-state path is empty")
	}
	if c == nil || c.ValidAfter.IsZero() || len(c.Raw()) == 0 {
		return fmt.Errorf("council: cannot record an incomplete served consensus")
	}
	lockPath := path + ".lock"
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("council: consensus serving lock %s exists; another server may be installing a file (if it crashed, inspect state before removing the lock)", lockPath)
		}
		return fmt.Errorf("council: creating consensus serving lock %s: %w", lockPath, err)
	}
	defer os.Remove(lockPath)

	want := servingState{
		Version:    servingStateVersion,
		ValidAfter: c.ValidAfter.UTC().Format(time.RFC3339),
		BodySHA256: directory.ConsensusBodyID(c.Raw()),
	}
	previous, exists, err := readServingState(path)
	if err != nil {
		return err
	}
	if exists {
		previousEpoch, err := time.Parse(time.RFC3339, previous.ValidAfter)
		if err != nil || previousEpoch.UTC().Format(time.RFC3339) != previous.ValidAfter {
			return fmt.Errorf("council: serving state %s has invalid valid_after %q", path, previous.ValidAfter)
		}
		switch {
		case previousEpoch.After(c.ValidAfter):
			return fmt.Errorf("council: refusing served-consensus rollback from epoch %s to %s",
				previous.ValidAfter, want.ValidAfter)
		case previousEpoch.Equal(c.ValidAfter) && previous.BodySHA256 != want.BodySHA256:
			return fmt.Errorf("council: refusing conflicting body %s for served epoch %s (persistent state records %s)",
				want.BodySHA256, want.ValidAfter, previous.BodySHA256)
		case previousEpoch.Equal(c.ValidAfter):
			return nil
		}
	}
	encoded, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		return fmt.Errorf("council: encoding consensus serving state: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeAtomic(path, encoded, 0o600); err != nil {
		return fmt.Errorf("council: recording consensus serving state: %w", err)
	}
	return nil
}

func readServingState(path string) (servingState, bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return servingState{}, false, nil
	}
	if err != nil {
		return servingState{}, false, fmt.Errorf("council: reading consensus serving state %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return servingState{}, false, fmt.Errorf("council: reading consensus serving state %s: %w", path, err)
	}
	if len(data) > 4096 {
		return servingState{}, false, fmt.Errorf("council: consensus serving state %s is unexpectedly large", path)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var state servingState
	if err := dec.Decode(&state); err != nil {
		return servingState{}, false, fmt.Errorf("council: parsing consensus serving state %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return servingState{}, false, fmt.Errorf("council: serving state %s has trailing content", path)
	}
	if state.Version != servingStateVersion {
		return servingState{}, false, fmt.Errorf("council: serving state %s has version %d, want %d", path, state.Version, servingStateVersion)
	}
	digest, err := hex.DecodeString(state.BodySHA256)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != state.BodySHA256 {
		return servingState{}, false, fmt.Errorf("council: serving state %s has invalid body_sha256 %q", path, state.BodySHA256)
	}
	canonical, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return servingState{}, false, fmt.Errorf("council: encoding consensus serving state: %w", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) {
		return servingState{}, false, fmt.Errorf("council: serving state %s is not canonically encoded", path)
	}
	return state, true, nil
}
