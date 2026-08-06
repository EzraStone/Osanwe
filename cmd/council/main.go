// Command council runs a directory authority.
//
// It reads relay descriptors from a directory on disk, verifies each one
// against the relay's own signing key, assembles a consensus, signs it, and
// serves it over HTTP.
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
	fs := flag.NewFlagSet("council", flag.ContinueOnError)
	addr := fs.String("addr", ":9000", "address to serve the consensus on")
	descDir := fs.String("descriptors", "./descriptors", "directory of relay descriptor files")
	identityPath := fs.String("identity", "./council.key", "this authority's signing key")
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
		fmt.Fprintf(os.Stderr, "Usage:\n  council -descriptors ./descriptors\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
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
		lifetime:       *lifetime,
		log:            log,
		probe:          *probe,
		unhealthyAfter: *unhealthyAfter,
		checker:        &health.Checker{Timeout: *probeTimeout},
		tracker:        health.NewTracker(),
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
		"  this key decides which relays clients can see.\n\n", id.Fingerprint())

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
	dir      string
	id       *directory.Identity
	lifetime time.Duration
	log      *slog.Logger

	probe          bool
	unhealthyAfter int
	checker        *health.Checker
	tracker        *health.Tracker

	mu      sync.RWMutex
	current []byte
	built   time.Time
	count   int
}

func (p *publisher) loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
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
	descriptors, err := loadDescriptors(p.dir, p.log)
	if err != nil {
		return err
	}
	if p.probe {
		descriptors = p.healthy(descriptors)
	}

	now := time.Now()
	c := &directory.Consensus{
		ValidAfter: now,
		ValidUntil: now.Add(p.lifetime),
		Relays:     descriptors,
	}
	if err := c.Sign(p.id); err != nil {
		return err
	}
	encoded, err := c.Encode()
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.current, p.built, p.count = encoded, now, len(descriptors)
	p.mu.Unlock()

	p.log.Info("published consensus", "relays", len(descriptors), "valid_until", c.ValidUntil.UTC().Format(time.RFC3339))
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

		// Rebuild immediately so a relay that just published does not have to
		// wait out a rebuild interval before clients can find it.
		if err := pub.rebuild(); err != nil {
			log.Error("rebuild after submission failed", "error", err)
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
func loadDescriptors(dir string, log *slog.Logger) ([]*directory.Descriptor, error) {
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

	now := time.Now()
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn("skipping descriptor", "file", name, "error", err)
			continue
		}
		d, err := directory.ParseDescriptor(data)
		if err != nil {
			log.Warn("skipping descriptor", "file", name, "error", err)
			continue
		}
		if d.Expired(now) {
			log.Warn("skipping expired descriptor", "file", name, "nickname", d.Nickname)
			continue
		}
		// One identity, one entry. Otherwise a relay could occupy several
		// slots and raise its odds of being selected by clients.
		if prev, dup := seen[d.Identity]; dup {
			log.Warn("skipping duplicate identity", "file", name, "nickname", d.Nickname, "already_listed_as", prev)
			continue
		}
		seen[d.Identity] = d.Nickname
		out = append(out, d)
	}
	return out, nil
}
