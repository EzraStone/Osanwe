package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
)

// repeatedFlag makes -authority and -part repeatable without pulling in a
// command-line dependency.
type repeatedFlag []string

func (v *repeatedFlag) String() string { return fmt.Sprint([]string(*v)) }
func (v *repeatedFlag) Set(s string) error {
	*v = append(*v, s)
	return nil
}

func runBuild(args []string, clock func() time.Time) error {
	fs := flag.NewFlagSet("council build", flag.ContinueOnError)
	identityPath := fs.String("identity", "./council.key", "authority signing key")
	signingState := fs.String("signing-state", "", "persistent anti-equivocation state (default: <identity>.consensus-state)")
	descDir := fs.String("descriptors", "./descriptors", "directory of signed relay descriptors")
	outPath := fs.String("out", "-", "partial consensus output file, or - for stdout")
	epoch := fs.Duration("epoch", 30*time.Minute, "deterministic consensus epoch")
	lifetime := fs.Duration("lifetime", 3*time.Hour, "consensus validity period")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: council build -identity council.key -descriptors descriptors -out authority-a.consensus")
		fmt.Fprintln(fs.Output(), "Build this epoch's canonical consensus body and add this authority's signature.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("council build: unexpected arguments: %v", fs.Args())
	}

	id, created, err := directory.LoadOrCreateIdentity(*identityPath)
	if err != nil {
		return fmt.Errorf("council build: loading identity: %w", err)
	}
	if created {
		fmt.Fprintf(os.Stderr, "generated new authority identity at %s (%s)\n", *identityPath, id.Fingerprint())
	}
	now := clock()
	window, err := directory.NewEpochConsensus(nil, now, *epoch, *lifetime)
	if err != nil {
		return fmt.Errorf("council build: %w", err)
	}
	relays, err := loadDescriptorsStrict(*descDir, workflowLogger(), window.ValidAfter, window.ValidUntil)
	if err != nil {
		return err
	}
	c, err := directory.NewEpochConsensus(relays, now, *epoch, *lifetime)
	if err != nil {
		return fmt.Errorf("council build: %w", err)
	}
	encoded, err := signConsensusWithState(signingStatePath(*signingState, *identityPath), id, c)
	if err != nil {
		return fmt.Errorf("council build: %w", err)
	}
	if err := writeDocument(*outPath, encoded); err != nil {
		return fmt.Errorf("council build: %w", err)
	}
	fmt.Fprintf(os.Stderr, "built partial %s for epoch %s with %d relay(s), signed by %s\n",
		directory.ConsensusBodyID(c.Raw()), c.ValidAfter.UTC().Format(time.RFC3339), len(c.Relays), id.Fingerprint())
	return nil
}

func runCosign(args []string, clock func() time.Time) error {
	fs := flag.NewFlagSet("council cosign", flag.ContinueOnError)
	identityPath := fs.String("identity", "./council.key", "authority signing key")
	signingState := fs.String("signing-state", "", "persistent anti-equivocation state (default: <identity>.consensus-state)")
	descDir := fs.String("descriptors", "./descriptors", "directory of locally approved signed relay descriptors")
	inPath := fs.String("in", "", "partial consensus to inspect and co-sign")
	outPath := fs.String("out", "-", "co-signed partial output file, or - for stdout")
	epoch := fs.Duration("epoch", 30*time.Minute, "agreed deterministic consensus epoch")
	lifetime := fs.Duration("lifetime", 3*time.Hour, "agreed consensus validity period")
	var authorityFlags repeatedFlag
	fs.Var(&authorityFlags, "authority", "trusted authority public key; repeat for each authority")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: council cosign -in authority-a.consensus -out authority-b.consensus -identity b.key -descriptors descriptors -authority KEY ...")
		fmt.Fprintln(fs.Output(), "Verify the proposer, epoch, and exact local relay set before adding a signature.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("council cosign: unexpected arguments: %v", fs.Args())
	}
	if *inPath == "" {
		return fmt.Errorf("council cosign: -in is required")
	}
	authorities, err := directory.AuthoritySet(authorityFlags)
	if err != nil {
		return fmt.Errorf("council cosign: %w", err)
	}
	id, err := directory.LoadIdentity(*identityPath)
	if err != nil {
		return fmt.Errorf("council cosign: loading identity: %w", err)
	}
	partial, err := readDocument(*inPath)
	if err != nil {
		return fmt.Errorf("council cosign: %w", err)
	}
	now := clock()
	proposal, err := directory.ParseConsensusPartial(partial, now)
	if err != nil {
		return fmt.Errorf("council cosign: refusing invalid partial: %w", err)
	}
	if err := directory.ValidateConsensusEpoch(proposal, *epoch, *lifetime); err != nil {
		return fmt.Errorf("council cosign: %w", err)
	}
	if current := now.UTC().Truncate(*epoch); !proposal.ValidAfter.Equal(current) {
		return fmt.Errorf("council cosign: refusing non-current epoch %s; current epoch is %s",
			proposal.ValidAfter.UTC().Format(time.RFC3339), current.Format(time.RFC3339))
	}
	relays, err := loadDescriptorsStrict(*descDir, workflowLogger(), proposal.ValidAfter, proposal.ValidUntil)
	if err != nil {
		return err
	}
	c, err := directory.PrepareConsensusCoSign(partial, id, relays, authorities, now, *epoch, *lifetime)
	if err != nil {
		return fmt.Errorf("council cosign: %w", err)
	}
	encoded, err := signConsensusWithState(signingStatePath(*signingState, *identityPath), id, c)
	if err != nil {
		return fmt.Errorf("council cosign: %w", err)
	}
	if err := writeDocument(*outPath, encoded); err != nil {
		return fmt.Errorf("council cosign: %w", err)
	}
	fmt.Fprintf(os.Stderr, "co-signed partial %s; it now carries %d authorized signature(s)\n",
		directory.ConsensusBodyID(c.Raw()), len(c.Signatures))
	return nil
}

func runAggregate(args []string, clock func() time.Time) error {
	fs := flag.NewFlagSet("council aggregate", flag.ContinueOnError)
	outPath := fs.String("out", "-", "final consensus output file, or - for stdout")
	threshold := fs.Int("threshold", 2, "minimum distinct authority signatures required")
	epoch := fs.Duration("epoch", 30*time.Minute, "agreed deterministic consensus epoch")
	lifetime := fs.Duration("lifetime", 3*time.Hour, "agreed consensus validity period")
	var authorityFlags repeatedFlag
	var partFlags repeatedFlag
	fs.Var(&authorityFlags, "authority", "trusted authority public key; repeat for each authority")
	fs.Var(&partFlags, "part", "partial consensus file; repeat for each file")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: council aggregate -part a.consensus -part b.consensus -out consensus -authority KEY_A -authority KEY_B -threshold 2")
		fmt.Fprintln(fs.Output(), "Combine signatures only when every partial signs the same exact body.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("council aggregate: unexpected arguments: %v", fs.Args())
	}
	if len(partFlags) == 0 {
		return fmt.Errorf("council aggregate: at least one -part is required")
	}
	authorities, err := directory.AuthoritySet(authorityFlags)
	if err != nil {
		return fmt.Errorf("council aggregate: %w", err)
	}
	parts := make([][]byte, 0, len(partFlags))
	for _, path := range partFlags {
		data, err := readDocument(path)
		if err != nil {
			return fmt.Errorf("council aggregate: %s: %w", path, err)
		}
		parts = append(parts, data)
	}
	now := clock()
	encoded, err := directory.MergeConsensus(parts, authorities, *threshold, now)
	if err != nil {
		return fmt.Errorf("council aggregate: %w", err)
	}
	c, err := directory.ParseConsensus(encoded, authorities, *threshold, now)
	if err != nil {
		return fmt.Errorf("council aggregate: verifying output: %w", err)
	}
	if err := directory.ValidateConsensusEpoch(c, *epoch, *lifetime); err != nil {
		return fmt.Errorf("council aggregate: %w", err)
	}
	if err := writeDocument(*outPath, encoded); err != nil {
		return fmt.Errorf("council aggregate: %w", err)
	}
	fmt.Fprintf(os.Stderr, "aggregated final consensus %s for epoch %s with %d signature(s)\n",
		directory.ConsensusBodyID(c.Raw()), c.ValidAfter.UTC().Format(time.RFC3339), len(c.Signatures))
	return nil
}

func runConsensusServer(args []string) error {
	fs := flag.NewFlagSet("council serve", flag.ContinueOnError)
	addr := fs.String("addr", ":9000", "address to serve on")
	consensusPath := fs.String("consensus", "", "aggregated consensus file to verify and serve")
	serveState := fs.String("serve-state", "", "persistent rollback state (default: <consensus>.serve-state)")
	reload := fs.Duration("reload", 10*time.Second, "how often to check the consensus file")
	threshold := fs.Int("threshold", 2, "minimum distinct authority signatures required")
	epoch := fs.Duration("epoch", 30*time.Minute, "agreed deterministic consensus epoch")
	lifetime := fs.Duration("lifetime", 3*time.Hour, "agreed consensus validity period")
	var authorityFlags repeatedFlag
	fs.Var(&authorityFlags, "authority", "trusted authority public key; repeat for each authority")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: council serve -consensus consensus -authority KEY_A -authority KEY_B -threshold 2 -addr :9000")
		fmt.Fprintln(fs.Output(), "Verify, reload, and publish a finalized M-of-N consensus.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("council serve: unexpected arguments: %v", fs.Args())
	}
	if *consensusPath == "" {
		return fmt.Errorf("council serve: -consensus is required")
	}
	if *consensusPath == "-" {
		return fmt.Errorf("council serve: -consensus must be a reloadable file, not stdin")
	}
	if *reload <= 0 {
		return fmt.Errorf("council serve: -reload must be positive")
	}
	authorities, err := directory.AuthoritySet(authorityFlags)
	if err != nil {
		return fmt.Errorf("council serve: %w", err)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	pub := &consensusFilePublisher{
		path:         *consensusPath,
		servingState: servingStatePath(*serveState, *consensusPath),
		authorities:  authorities,
		threshold:    *threshold,
		epoch:        *epoch,
		lifetime:     *lifetime,
		log:          log,
		now:          time.Now,
	}
	if err := pub.reload(); err != nil {
		return fmt.Errorf("council serve: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/consensus", pub.serve)
	mux.HandleFunc("/healthz", pub.health)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go pub.loop(ctx, *reload)
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	log.Info("serving finalized consensus", "addr", *addr, "path", *consensusPath, "threshold", *threshold)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// consensusFilePublisher installs only verified roll-forwards. A replacement
// for an already-seen epoch must have the exact same body; a lower epoch is a
// rollback even while its signatures and validity window remain good.
type consensusFilePublisher struct {
	path         string
	servingState string
	authorities  map[string]ed25519.PublicKey
	threshold    int
	epoch        time.Duration
	lifetime     time.Duration
	log          *slog.Logger
	now          func() time.Time

	mu      sync.RWMutex
	current []byte
	parsed  *directory.Consensus
}

func (p *consensusFilePublisher) reload() error {
	data, err := readDocument(p.path)
	if err != nil {
		return err
	}
	now := p.now()
	// A one-part merge performs strict authority and signature validation and
	// re-encodes signature lines canonically. It requires threshold just like a
	// client, while also refusing stray unconfigured signers.
	verified, err := directory.MergeConsensus([][]byte{data}, p.authorities, p.threshold, now)
	if err != nil {
		return fmt.Errorf("verifying %s: %w", p.path, err)
	}
	c, err := directory.ParseConsensus(verified, p.authorities, p.threshold, now)
	if err != nil {
		return fmt.Errorf("verifying %s: %w", p.path, err)
	}
	if err := directory.ValidateConsensusEpoch(c, p.epoch, p.lifetime); err != nil {
		return fmt.Errorf("verifying %s: %w", p.path, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parsed != nil {
		switch {
		case c.ValidAfter.Before(p.parsed.ValidAfter):
			return fmt.Errorf("refusing consensus rollback from epoch %s to %s",
				p.parsed.ValidAfter.UTC().Format(time.RFC3339), c.ValidAfter.UTC().Format(time.RFC3339))
		case c.ValidAfter.Equal(p.parsed.ValidAfter) && !bytes.Equal(c.Raw(), p.parsed.Raw()):
			return fmt.Errorf("refusing conflicting body %s for current epoch %s (already serving %s)",
				directory.ConsensusBodyID(c.Raw()), c.ValidAfter.UTC().Format(time.RFC3339), directory.ConsensusBodyID(p.parsed.Raw()))
		}
	}
	if err := recordServedConsensus(p.servingState, c); err != nil {
		return err
	}
	if bytes.Equal(verified, p.current) {
		return nil
	}
	p.current = verified
	p.parsed = c
	p.log.Info("installed finalized consensus", "body", directory.ConsensusBodyID(c.Raw()),
		"epoch", c.ValidAfter.UTC().Format(time.RFC3339), "valid_until", c.ValidUntil.UTC().Format(time.RFC3339),
		"relays", len(c.Relays), "signatures", len(c.Signatures))
	return nil
}

func (p *consensusFilePublisher) loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.reload(); err != nil {
				p.log.Error("consensus reload failed; retaining the last verified document", "error", err)
			}
		}
	}
}

func (p *consensusFilePublisher) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "GET the finalized consensus here", http.StatusMethodNotAllowed)
		return
	}
	p.mu.RLock()
	body := append([]byte(nil), p.current...)
	c := p.parsed
	p.mu.RUnlock()
	if c == nil || !c.Fresh(p.now()) {
		http.Error(w, "no fresh finalized consensus available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+directory.ConsensusBodyID(c.Raw())+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func (p *consensusFilePublisher) health(w http.ResponseWriter, _ *http.Request) {
	p.mu.RLock()
	c := p.parsed
	p.mu.RUnlock()
	if c == nil || !c.Fresh(p.now()) {
		http.Error(w, "consensus stale", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

func readDocument(path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, directory.MaxConsensusSize+1))
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		if len(data) > directory.MaxConsensusSize {
			return nil, fmt.Errorf("consensus larger than %d bytes", directory.MaxConsensusSize)
		}
		return data, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, directory.MaxConsensusSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) > directory.MaxConsensusSize {
		return nil, fmt.Errorf("consensus %s is larger than %d bytes", path, directory.MaxConsensusSize)
	}
	return data, nil
}

func writeDocument(path string, data []byte) error {
	if path == "-" {
		if _, err := os.Stdout.Write(data); err != nil {
			return fmt.Errorf("writing stdout: %w", err)
		}
		return nil
	}
	return writeAtomic(path, data, 0o644)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".consensus-*")
	if err != nil {
		return fmt.Errorf("creating temporary output beside %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("setting output mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing temporary output: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temporary output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary output: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("installing %s: %w", path, err)
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening output directory for sync: %w", err)
	}
	if err := dirHandle.Sync(); err != nil {
		dirHandle.Close()
		return fmt.Errorf("syncing output directory: %w", err)
	}
	if err := dirHandle.Close(); err != nil {
		return fmt.Errorf("closing output directory: %w", err)
	}
	ok = true
	return nil
}

func workflowLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func flagError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}
