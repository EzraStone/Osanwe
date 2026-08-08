// Command council runs a directory authority.
//
// It reads relay descriptors from a directory on disk, verifies each one
// against the relay's own signing key, and signs consensus documents. Its
// build, cosign, aggregate, and serve subcommands provide the complete M-of-N
// file workflow; the flag-only form runs a single online authority.
//
//	council -descriptors ./descriptors -identity ./council.key
//
// A single council is not a network. One authority means one machine whose
// compromise lets an attacker decide which relays every client can see, so a
// real deployment runs several, operated by different people in different
// jurisdictions, and clients require agreement from more than one. This binary
// deliberately warns when it notices it is the only signer it knows about.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
	"github.com/EzraStone/osanwe/internal/health"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:])
}

func runArgs(args []string) error {
	return runArgsAt(args, time.Now)
}

func runArgsAt(args []string, now func() time.Time) error {
	if len(args) > 0 {
		switch args[0] {
		case "build":
			return runBuild(args[1:], now)
		case "cosign":
			return runCosign(args[1:], now)
		case "aggregate":
			return runAggregate(args[1:], now)
		case "serve":
			return runConsensusServer(args[1:])
		}
		if !strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("council: unknown subcommand %q (want build, cosign, aggregate, or serve)", args[0])
		}
	}
	return runAuthority(args)
}

func runAuthority(args []string) error {
	fs := flag.NewFlagSet("council", flag.ContinueOnError)
	addr := fs.String("addr", ":9000", "address to serve the consensus on")
	descDir := fs.String("descriptors", "./descriptors", "directory of relay descriptor files")
	identityPath := fs.String("identity", "./council.key", "this authority's signing key")
	signingState := fs.String("signing-state", "", "persistent anti-equivocation state (default: <identity>.consensus-state)")
	lifetime := fs.Duration("lifetime", 3*time.Hour, "how long each consensus stays valid")
	rebuild := fs.Duration("rebuild", 30*time.Minute, "how often to rebuild the consensus")
	probe := fs.Bool("probe", true, "check that each relay is reachable and presenting the key its descriptor claims")
	probeTimeout := fs.Duration("probe-timeout", 10*time.Second, "how long a single relay probe may take")
	unhealthyAfter := fs.Int("unhealthy-after", 3, "consecutive failed probes before a relay is dropped from the consensus")
	acceptPath := fs.String("accept", "", "file listing relay identities this authority will publish. Required to enable POST /publish")
	showKey := fs.Bool("key", false, "print this authority's public key and exit")
	verbose := fs.Bool("v", false, "verbose logging")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "council — an Osanwë directory authority.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  council -descriptors ./descriptors\n  council build|cosign|aggregate|serve -h\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	id, created, err := directory.LoadOrCreateIdentity(*identityPath)
	if err != nil {
		return err
	}
	if *showKey {
		fmt.Println(id.Fingerprint())
		return nil
	}
	if created {
		log.Info("generated a new authority identity", "path", *identityPath)
	}

	if *lifetime <= 0 {
		return errors.New("council: -lifetime must be positive; a consensus with no validity window could be replayed forever")
	}
	if *rebuild >= *lifetime {
		return fmt.Errorf("council: -rebuild (%s) must be shorter than -lifetime (%s), or the consensus expires before it is replaced",
			*rebuild, *lifetime)
	}

	if *unhealthyAfter < 1 {
		return errors.New("council: -unhealthy-after must be at least 1; dropping a relay on zero failures would drop every relay")
	}

	var store *directory.Store
	if *acceptPath != "" {
		list, err := directory.NewAcceptList(*acceptPath)
		if err != nil {
			return err
		}
		store = &directory.Store{Dir: *descDir, Accept: list}
		log.Info("descriptor submission enabled", "accept_list", *acceptPath, "identities", list.Len())
		if list.Len() == 0 {
			log.Warn("the accept list is empty, so every submission will be refused until an identity is added")
		}
	} else {
		log.Info("descriptor submission disabled; pass -accept to enable POST /publish")
	}

	pub := &publisher{
		dir:            *descDir,
		id:             id,
		signingState:   signingStatePath(*signingState, *identityPath),
		epoch:          *rebuild,
		lifetime:       *lifetime,
		log:            log,
		probe:          *probe,
		unhealthyAfter: *unhealthyAfter,
		checker:        &health.Checker{Timeout: *probeTimeout},
		tracker:        health.NewTracker(),
		now:            time.Now,
	}
	if !*probe {
		log.Warn("relay probing is off; the consensus may advertise relays that are down or whose keys have changed")
	}
	if err := pub.rebuild(); err != nil {
		return err
	}

	log.Info("council starting",
		"addr", *addr,
		"key", id.Fingerprint(),
		"descriptors", *descDir,
		"lifetime", lifetime.String())

	fmt.Fprintf(os.Stderr, "\n  authority key: %s\n\n"+
		"  Clients need this key, and should require agreement from more than one\n"+
		"  authority. A single council is a single point of failure: whoever holds\n"+
		"  this key decides which relays clients can see. This endpoint publishes\n"+
		"  one partial signature; use `council aggregate` and `council serve` before\n"+
		"  configuring a client threshold greater than one.\n\n", id.Fingerprint())

	mux := http.NewServeMux()
	mux.HandleFunc("/consensus", pub.serve)
	mux.HandleFunc("/publish", submitHandler(store, pub, log))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go pub.loop(ctx, *rebuild)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// publisher rebuilds and serves the consensus.
type publisher struct {
	dir          string
	id           *directory.Identity
	signingState string
	epoch        time.Duration
	lifetime     time.Duration
	log          *slog.Logger

	probe          bool
	unhealthyAfter int
	checker        *health.Checker
	tracker        *health.Tracker
	now            func() time.Time

	mu         sync.RWMutex
	current    []byte
	signedBody []byte
	validAfter time.Time
	built      time.Time
	count      int
}

type epochBodyConflictError struct {
	epoch     time.Time
	candidate string
	current   string
}

func (e *epochBodyConflictError) Error() string {
	return fmt.Sprintf("council: relay set changed during epoch %s; refusing to sign conflicting body %s after body %s (the change will enter the next epoch)",
		e.epoch.UTC().Format(time.RFC3339), e.candidate, e.current)
}

func (p *publisher) loop(ctx context.Context, every time.Duration) {
	for {
		// Rebuild on shared UTC epoch boundaries, rather than every `every`
		// after this particular process happened to start. Authorities with the
		// same settings therefore advance to the next body together.
		next := time.Now().UTC().Truncate(every).Add(every)
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			if err := p.rebuild(); err != nil {
				// Keep serving the previous consensus rather than nothing. It
				// is signed and still fresh; an empty response would take
				// every client offline over a transient disk error.
				p.log.Error("rebuild failed, continuing to serve the previous consensus", "error", err)
			}
		}
	}
}

func (p *publisher) rebuild() error {
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	window, err := directory.NewEpochConsensus(nil, now, p.epoch, p.lifetime)
	if err != nil {
		return err
	}
	descriptors, err := loadDescriptors(p.dir, p.log, window.ValidAfter, window.ValidUntil)
	if err != nil {
		return err
	}
	if p.probe {
		descriptors = p.healthy(descriptors)
	}

	c, err := directory.NewEpochConsensus(descriptors, now, p.epoch, p.lifetime)
	if err != nil {
		return err
	}

	p.mu.Lock()
	if !p.validAfter.IsZero() {
		switch {
		case c.ValidAfter.Before(p.validAfter):
			p.mu.Unlock()
			return fmt.Errorf("council: refusing to sign epoch rollback from %s to %s",
				p.validAfter.UTC().Format(time.RFC3339), c.ValidAfter.UTC().Format(time.RFC3339))
		case c.ValidAfter.Equal(p.validAfter) && !bytes.Equal(c.Raw(), p.signedBody):
			p.mu.Unlock()
			return &epochBodyConflictError{
				epoch:     c.ValidAfter,
				candidate: directory.ConsensusBodyID(c.Raw()),
				current:   directory.ConsensusBodyID(p.signedBody),
			}
		}
	}
	encoded, err := signConsensusWithState(p.signingState, p.id, c)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	p.current, p.built, p.count = encoded, now, len(descriptors)
	p.signedBody = c.Raw()
	p.validAfter = c.ValidAfter
	p.mu.Unlock()

	p.log.Info("published partial consensus", "body", directory.ConsensusBodyID(c.Raw()),
		"relays", len(descriptors), "valid_until", c.ValidUntil.UTC().Format(time.RFC3339))
	if len(descriptors) == 0 {
		p.log.Warn("consensus lists no relays; clients fetching it will find nothing to connect to")
	}
	return nil
}

// maxSubmission bounds a POSTed descriptor. Descriptors are small; anything
// larger is either a mistake or an attempt to make the authority read forever.
const maxSubmission = 64 << 10

// submitHandler accepts a signed descriptor from a relay.
//
// Publication is default-deny and stays that way: without -accept the endpoint
// refuses everything. An open endpoint would let anyone register relays, and a
// directory listing a thousand attacker-run relays beside three honest ones has
// handed the attacker almost every client without breaking one signature.
func submitHandler(store *directory.Store, pub *publisher, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST a signed descriptor here", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "this authority does not accept submissions; its operator must configure an accept list", http.StatusServiceUnavailable)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxSubmission+1))
		if err != nil {
			http.Error(w, "could not read the request body", http.StatusBadRequest)
			return
		}
		if len(body) > maxSubmission {
			http.Error(w, fmt.Sprintf("descriptor larger than %d bytes", maxSubmission), http.StatusRequestEntityTooLarge)
			return
		}

		// Parsing is what verifies the signature, so nothing below trusts the
		// submitter's claims about itself.
		d, err := directory.ParseDescriptor(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Re-read the accept list so an operator can admit a relay without
		// restarting the authority.
		if err := store.Accept.Reload(); err != nil {
			log.Error("could not reload the accept list", "error", err)
		}

		if err := store.Put(d, d.Encoded(), time.Now()); err != nil {
			var notAccepted *directory.ErrNotAccepted
			var stale *directory.ErrStale
			switch {
			case errors.As(err, &notAccepted):
				log.Warn("refused submission from an identity that is not admitted",
					"nickname", d.Nickname, "identity", d.Identity)
				http.Error(w, err.Error(), http.StatusForbidden)
			case errors.As(err, &stale):
				http.Error(w, err.Error(), http.StatusConflict)
			default:
				log.Error("could not store submission", "nickname", d.Nickname, "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}

		log.Info("accepted descriptor", "nickname", d.Nickname, "address", d.Address, "identity", d.Identity)

		// Re-evaluate immediately. If this epoch already has a signed body, the
		// anti-equivocation rule deliberately queues the change for the next one.
		if err := pub.rebuild(); err != nil {
			var conflict *epochBodyConflictError
			if errors.As(err, &conflict) {
				log.Info("descriptor accepted for the next consensus epoch",
					"nickname", d.Nickname, "epoch", conflict.epoch.Add(pub.epoch).UTC().Format(time.RFC3339))
			} else {
				log.Error("rebuild after submission failed", "error", err)
			}
		}

		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "accepted %s (%s)\n", d.Nickname, d.Address)
	}
}

// healthy drops relays that have failed enough consecutive probes.
//
// A single failed probe is not enough. Networks are unreliable, and a
// directory that removed a relay the first time one timed out would flap
// constantly and take clients with it. Equally a relay that has been
// unreachable for hours should not stay listed, hence the threshold.
func (p *publisher) healthy(in []*directory.Descriptor) []*directory.Descriptor {
	if len(in) == 0 {
		return in
	}

	targets := make([]health.Target, 0, len(in))
	keep := make(map[string]bool, len(in))
	for _, d := range in {
		targets = append(targets, health.Target{Key: d.Identity, Address: d.Address, Pin: d.TLSPin})
		keep[d.Identity] = true
	}
	p.tracker.Forget(keep)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results := p.checker.ProbeAll(ctx, targets)

	out := make([]*directory.Descriptor, 0, len(in))
	for _, d := range in {
		res := results[d.Identity]
		fails := p.tracker.Record(d.Identity, res.OK)
		if res.OK {
			out = append(out, d)
			continue
		}
		if fails < p.unhealthyAfter {
			// Still listed, but say so, so an operator sees trouble building
			// before the relay disappears.
			p.log.Warn("relay probe failed, still listing it",
				"nickname", d.Nickname, "consecutive_failures", fails,
				"drops_after", p.unhealthyAfter, "error", res.Err)
			out = append(out, d)
			continue
		}
		p.log.Warn("dropping unhealthy relay from the consensus",
			"nickname", d.Nickname, "consecutive_failures", fails, "error", res.Err)
	}
	return out
}

func (p *publisher) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	body := p.current
	p.mu.RUnlock()

	if len(body) == 0 {
		http.Error(w, "no consensus available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// loadDescriptors reads and verifies every descriptor in a directory.
//
// A descriptor that fails verification is skipped with a warning rather than
// aborting the rebuild: one relay operator publishing a broken file must not
// be able to take the whole directory down.
func loadDescriptors(dir string, log *slog.Logger, validAfter, validUntil time.Time) ([]*directory.Descriptor, error) {
	return loadDescriptorsMode(dir, log, false, validAfter, validUntil)
}

// loadDescriptorsStrict is used by the offline signing workflow. A daemon can
// skip one temporarily broken file and remain available, but an operator about
// to cast a durable consensus vote should resolve every local ambiguity first.
func loadDescriptorsStrict(dir string, log *slog.Logger, validAfter, validUntil time.Time) ([]*directory.Descriptor, error) {
	return loadDescriptorsMode(dir, log, true, validAfter, validUntil)
}

func loadDescriptorsMode(dir string, log *slog.Logger, strict bool, validAfter, validUntil time.Time) ([]*directory.Descriptor, error) {
	if validAfter.IsZero() || validUntil.IsZero() || !validUntil.After(validAfter) {
		return nil, fmt.Errorf("council: invalid descriptor selection window %s to %s", validAfter, validUntil)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("council: reading descriptor directory: %w", err)
	}

	var out []*directory.Descriptor
	seen := map[string]string{} // identity -> nickname

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("council: reading descriptor %s: %w", name, err)
			}
			log.Warn("skipping descriptor", "file", name, "error", err)
			continue
		}
		d, err := directory.ParseDescriptor(data)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("council: invalid descriptor %s: %w", name, err)
			}
			log.Warn("skipping descriptor", "file", name, "error", err)
			continue
		}
		if !d.ValidThroughout(validAfter, validUntil) {
			log.Warn("skipping descriptor that is not valid for the complete consensus window",
				"file", name, "nickname", d.Nickname,
				"valid_after", validAfter.UTC().Format(time.RFC3339),
				"valid_until", validUntil.UTC().Format(time.RFC3339))
			continue
		}
		// One identity, one entry. Otherwise a relay could occupy several
		// slots and raise its odds of being selected by clients.
		if prev, dup := seen[d.Identity]; dup {
			if strict {
				return nil, fmt.Errorf("council: descriptor %s repeats identity %s already listed as %s", name, d.Identity, prev)
			}
			log.Warn("skipping duplicate identity", "file", name, "nickname", d.Nickname, "already_listed_as", prev)
			continue
		}
		seen[d.Identity] = d.Nickname
		out = append(out, d)
	}
	return out, nil
}
