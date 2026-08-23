package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxClientConfigBytes = 64 << 10

// clientFileConfig contains connection facts that are safe to keep on disk.
// Relay secrets, receipts, API keys and tokens are intentionally not fields;
// DisallowUnknownFields makes attempts to add them fail visibly.
type clientFileConfig struct {
	SchemaVersion int      `json:"schema_version"`
	Relay         string   `json:"relay,omitempty"`
	Pin           string   `json:"pin,omitempty"`
	Directories   []string `json:"directories,omitempty"`
	Authorities   []string `json:"authorities,omitempty"`
	Threshold     *int     `json:"threshold,omitempty"`
	Upstream      string   `json:"upstream,omitempty"`
	UpstreamCA    string   `json:"upstream_ca,omitempty"`
	Mint          string   `json:"mint,omitempty"`
	MintKeyID     string   `json:"mint_key_id,omitempty"`
}

func loadClientFileConfig(path string) (clientFileConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return clientFileConfig{}, fmt.Errorf("bearer: opening -config: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return clientFileConfig{}, fmt.Errorf("bearer: inspecting -config: %w", err)
	} else if info.Size() > maxClientConfigBytes {
		return clientFileConfig{}, fmt.Errorf("bearer: -config is %d bytes, maximum is %d", info.Size(), maxClientConfigBytes)
	}

	dec := json.NewDecoder(io.LimitReader(f, maxClientConfigBytes+1))
	dec.DisallowUnknownFields()
	var cfg clientFileConfig
	if err := dec.Decode(&cfg); err != nil {
		return clientFileConfig{}, fmt.Errorf("bearer: reading -config: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return clientFileConfig{}, errors.New("bearer: -config contains more than one JSON value")
		}
		return clientFileConfig{}, fmt.Errorf("bearer: reading trailing -config data: %w", err)
	}
	if cfg.SchemaVersion != 1 {
		return clientFileConfig{}, fmt.Errorf("bearer: -config schema_version is %d, want 1", cfg.SchemaVersion)
	}
	if cfg.UpstreamCA != "" && !filepath.IsAbs(cfg.UpstreamCA) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return clientFileConfig{}, fmt.Errorf("bearer: resolving -config path: %w", err)
		}
		cfg.UpstreamCA = filepath.Join(filepath.Dir(absolute), cfg.UpstreamCA)
	}
	return cfg, nil
}
