// Package pool keeps a client pointed at a relay that works.
//
// A consensus is a snapshot. Relays go down, operators stop them, and the
// authorities publish a new list every few minutes. A client that fetched once
// at startup and picked one relay would keep dialling a dead address until
// somebody noticed and restarted it -- and asking to be restarted is not
// something software running quietly in the background gets to do.
//
// So this package holds the current set of relays, refreshes it on a timer, and
// moves to a different relay when the current one actually stops answering.
//
// # Why the relay does not rotate
//
// The obvious design is to spread requests across every relay in the consensus.
// Tor learned the opposite lesson, and the reasoning transfers directly: a
// client that keeps choosing fresh entry relays will eventually choose a
// hostile one, and the chance of having used at least one grows with every
// rotation. Staying with a single relay -- Tor calls it a guard -- bounds that
// exposure instead of accumulating it.
//
// A relay already sees your address and your timing. Spreading traffic over
// five relays does not divide that knowledge; it hands the same knowledge to
// five parties. So selection happens once, and a new relay is chosen only when
// the current one fails.
package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
	"github.com/EzraStone/osanwe/internal/tunnel"
)

// Defaults.
const (
	// DefaultRefreshEvery is well under a consensus's lifetime, so a client
	// picks up a new document long before the one it holds goes stale.
	DefaultRefreshEvery = 15 * time.Minute

	// DefaultDialTimeout bounds one attempt, not the whole dial. A request that
	// has to walk past several dead relays should still fail in seconds.
	DefaultDialTimeout = 10 * time.Second

	// Backoff bounds for a relay that has failed. The cap matters: without one
	// a relay that failed a dozen times would be shunned for hours, long after
	// whatever broke it was fixed.
	minBackoff = 15 * time.Second
	maxBackoff = 10 * time.Minute
)

// Dialer opens a tunnel. internal/tunnel.Dialer satisfies it, and tests
// substitute their own.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Config configures a Pool.
type Config struct {
	// Fetcher retrieves and verifies consensus documents. Required.
	Fetcher *directory.Fetcher

	// Destination is the "host:port" a relay must be willing to carry, used to
	// filter the consensus down to relays that can serve this client at all.
	Destination string

	// Secret authenticates this client to a relay.
	Secret string

	// RefreshEvery is how often to re-fetch. Zero means DefaultRefreshEvery.
	RefreshEvery time.Duration

	// DialTimeout bounds a single relay attempt. Zero means DefaultDialTimeout.
	DialTimeout time.Duration

	Logger *slog.Logger

	// Now is overridable for tests.
	Now func() time.Time

	// newDialer builds the dialer for one relay. Tests replace it; production
	// leaves it nil and gets internal/tunnel.
	newDialer func(addr, pin, secret string, timeout time.Duration) (Dialer, error)
}

// Stats are cumulative counters. As elsewhere, they hold no per-request detail.
type Stats struct {
	Refreshes     int64
	RefreshErrors int64
	Failovers     int64
	DialErrors    int64
}

// relay is one candidate and what we have learned about it.
type relay struct {
	desc   *directory.Descriptor
	dialer Dialer

	fails     int
	downUntil time.Time
	lastErr   error
}

func (r *relay) key() string { return r.desc.Address + "|" + r.desc.TLSPin }

// Pool selects a relay and keeps that selection current.
type Pool struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu         sync.Mutex
	relays     []*relay
	guard      *relay
	guardSince time.Time
	signedBy   int
	stats      Stats
}

// New validates a Config and returns a Pool. It does not fetch; call Refresh.
func New(cfg Config) (*Pool, error) {
	if cfg.Fetcher == nil {
		return nil, errors.New("pool: Fetcher is required")
	}
	if cfg.Destination == "" {
		return nil, errors.New("pool: Destination is required; without it there is no way to tell which relays can serve this client")
	}
	if cfg.RefreshEvery <= 0 {
		cfg.RefreshEvery = DefaultRefreshEvery
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.newDialer == nil {
		cfg.newDialer = func(addr, pin, secret string, timeout time.Duration) (Dialer, error) {
			return tunnel.New(tunnel.Config{Relay: addr, Pin: pin, Secret: secret, Timeout: timeout})
		}
	}
	return &Pool{cfg: cfg, log: cfg.Logger, now: cfg.Now}, nil
}

// Refresh fetches a consensus and installs the relays that can serve this
// client.
//
// A failed refresh leaves the previous set in place. Going relay-less because
// an authority was briefly unreachable would convert a directory outage into a
// client outage, which is exactly backwards: the whole reason the consensus is
// signed is that it stays trustworthy while it is held.
func (p *Pool) Refresh(ctx context.Context) error {
	c, err := p.cfg.Fetcher.Fetch(ctx)
	if err != nil {
		p.mu.Lock()
		p.stats.RefreshErrors++
		held := len(p.relays)
		p.mu.Unlock()
		if held > 0 {
			p.log.Warn("keeping the relays already held; the directory could not be reached",
				"relays_held", held, "error", err)
		}
		return fmt.Errorf("pool: refreshing the directory: %w", err)
	}

	p.mu.Lock()
	p.signedBy = len(c.Signatures)
	p.mu.Unlock()

	usable := c.Usable(p.now(), p.cfg.Destination)
	if len(usable) == 0 {
		p.mu.Lock()
		p.stats.RefreshErrors++
		held := len(p.relays)
		p.mu.Unlock()
		if held > 0 {
			p.log.Warn("the new consensus lists no relay for this destination; keeping the previous set",
				"destination", p.cfg.Destination, "relays_held", held)
			return nil
		}
		return fmt.Errorf("pool: no relay in the consensus will carry traffic to %s", p.cfg.Destination)
	}

	return p.install(usable)
}

// install replaces the relay set, carrying forward what is already known.
//
// Failure counts and dialers survive a refresh when the same relay reappears
// with the same key. Rebuilding from scratch every refresh would erase the
// backoff on a relay that has been failing for an hour and send traffic back at
// it every fifteen minutes.
func (p *Pool) install(descs []*directory.Descriptor) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	prev := make(map[string]*relay, len(p.relays))
	for _, r := range p.relays {
		prev[r.key()] = r
	}

	next := make([]*relay, 0, len(descs))
	var errs []string
	for _, d := range descs {
		r := &relay{desc: d}
		if old, ok := prev[r.key()]; ok {
			// Same address and same key: the relay we already know.
			old.desc = d
			next = append(next, old)
			continue
		}
		dl, err := p.cfg.newDialer(d.Address, d.TLSPin, p.cfg.Secret, p.cfg.DialTimeout)
		if err != nil {
			// One malformed descriptor must not cost us the whole consensus.
			errs = append(errs, fmt.Sprintf("%s: %v", d.Nickname, err))
			continue
		}
		r.dialer = dl
		next = append(next, r)
	}

	if len(next) == 0 {
		p.stats.RefreshErrors++
		return fmt.Errorf("pool: no relay in the consensus was usable: %s", strings.Join(errs, "; "))
	}
	for _, e := range errs {
		p.log.Warn("skipping a relay in the consensus", "reason", e)
	}

	// Shuffle, because the consensus has a fixed order and taking the first
	// entry would put every client in the world on the same relay. That is bad
	// for load and worse for privacy: one relay carrying everybody's traffic
	// sees everybody's timing.
	//
	// This is the only randomness in selection. It decides which relay a client
	// starts on; after that the guard makes the choice sticky, so shuffling on
	// each refresh reorders the fallbacks without disturbing the relay in use.
	rand.Shuffle(len(next), func(i, j int) { next[i], next[j] = next[j], next[i] })

	p.relays = next
	p.stats.Refreshes++

	// Keep the guard if it is still listed. Dropping it on every refresh would
	// reintroduce rotation through the back door, one refresh at a time.
	//
	// Carrying relays forward above preserves the pointer, so identity is the
	// right test: the guard survives only if the same address and the same key
	// are still in the consensus. A relay that reappears with a rotated key is
	// a different relay and has to earn selection again.
	if p.guard != nil {
		found := false
		for _, r := range next {
			if r == p.guard {
				found = true
				break
			}
		}
		if !found {
			p.log.Info("the relay in use is no longer in the consensus; another will be chosen on the next request",
				"relay", p.guard.desc.Nickname)
			p.guard = nil
			p.guardSince = time.Time{}
		}
	}
	return nil
}

// Run refreshes on a timer until ctx is cancelled.
func (p *Pool) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.RefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.Refresh(ctx); err != nil && ctx.Err() == nil {
				p.log.Warn("directory refresh failed", "error", err)
			}
		}
	}
}

// DialContext opens a tunnel to addr, moving to another relay if the current
// one will not carry it.
//
// This satisfies bearer.Dialer, so failover is invisible to everything above:
// the proxy asks for a connection and gets one, without knowing how many relays
// were tried to produce it.
func (p *Pool) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	candidates, err := p.candidates()
	if err != nil {
		return nil, err
	}

	var failures []string
	for _, r := range candidates {
		// Dialling happens outside the lock: it is network I/O, and holding the
		// mutex across it would serialise every request in the process behind
		// one slow relay.
		conn, err := r.dialer.DialContext(ctx, network, addr)
		if err == nil {
			p.succeed(r)
			return conn, nil
		}
		if ctx.Err() != nil {
			// The caller gave up. That says nothing about the relay, so it must
			// not be penalised for it.
			return nil, err
		}
		p.fail(r, err)
		failures = append(failures, fmt.Sprintf("%s: %v", r.desc.Nickname, err))
	}
	return nil, summarise(addr, failures)
}

// candidates returns relays to try, best first.
func (p *Pool) candidates() ([]*relay, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.relays) == 0 {
		return nil, errors.New("pool: no relays known yet; the directory has not been fetched successfully")
	}
	now := p.now()

	var out []*relay
	// The guard goes first whenever it is not in backoff, which is what makes
	// the selection sticky rather than round-robin.
	if p.guard != nil && !now.Before(p.guard.downUntil) {
		out = append(out, p.guard)
	}
	for _, r := range p.relays {
		if r == p.guard || now.Before(r.downUntil) {
			continue
		}
		out = append(out, r)
	}

	// Everything is in backoff. Try anyway rather than refusing: a backoff is a
	// preference about ordering, and letting it harden into "this client will
	// not make requests" would be a worse failure than a slow one.
	if len(out) == 0 {
		out = append(out, p.relays...)
	}
	return out, nil
}

func (p *Pool) succeed(r *relay) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r.fails = 0
	r.downUntil = time.Time{}
	r.lastErr = nil
	if p.guard != r {
		if p.guard != nil {
			p.stats.Failovers++
			p.log.Info("moved to another relay", "from", p.guard.desc.Nickname, "to", r.desc.Nickname)
		}
		p.guard = r
		p.guardSince = p.now()
	}
}

func (p *Pool) fail(r *relay, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r.fails++
	r.lastErr = err
	r.downUntil = p.now().Add(backoff(r.fails))
	p.stats.DialErrors++

	// A pin mismatch is not an ordinary failure. It means the relay answering
	// that address is presenting a key the directory did not publish, which is
	// either a rotation the operator never announced or something impersonating
	// a relay. Failing over is right, but doing it silently would hide the one
	// event most worth seeing.
	if isPinMismatch(err) {
		p.log.Error("relay key does not match the one the directory published",
			"relay", r.desc.Nickname, "address", r.desc.Address)
		return
	}
	p.log.Warn("relay failed", "relay", r.desc.Nickname, "attempt", r.fails, "error", err)
}

func backoff(fails int) time.Duration {
	d := minBackoff
	for i := 1; i < fails && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

func isPinMismatch(err error) bool {
	return strings.Contains(err.Error(), "relay key mismatch")
}

// summarise turns a list of relay failures into something actionable.
func summarise(addr string, failures []string) error {
	// When every relay rejected the credential, the problem is the secret, not
	// the relays. Saying so directly saves reading four identical 407s and
	// concluding the network is down.
	all407 := len(failures) > 0
	for _, f := range failures {
		if !strings.Contains(f, "(407)") {
			all407 = false
			break
		}
	}
	if all407 {
		return fmt.Errorf("pool: every relay rejected the credential. "+
			"OSANWE_SECRET does not match what these relays were started with (%d tried)", len(failures))
	}
	return fmt.Errorf("pool: no relay could carry a connection to %s: %s", addr, strings.Join(failures, "; "))
}

// Current reports the relay in use, if one has been chosen.
func (p *Pool) Current() (nickname, address string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.guard == nil {
		return "", "", false
	}
	return p.guard.desc.Nickname, p.guard.desc.Address, true
}

// GuardSince reports when the relay in use was chosen. The bool is false when
// no relay has been selected yet.
func (p *Pool) GuardSince() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.guard == nil || p.guardSince.IsZero() {
		return time.Time{}, false
	}
	return p.guardSince, true
}

// SignedBy reports how many authorities signed the consensus in force.
//
// This is the number a user should actually be shown. The threshold is what
// the client demanded; this is what it got, and the two are not the same
// statement.
func (p *Pool) SignedBy() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.signedBy
}

// Len reports how many relays are currently known.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.relays)
}

// Stats returns a snapshot of the counters.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}
