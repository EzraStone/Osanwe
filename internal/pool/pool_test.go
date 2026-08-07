package pool

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
)

const dest = "api.anthropic.com:443"

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --------------------------------------------------------------------------
// a real signed consensus, served over HTTP
//
// The refresh path is not stubbed. Building and serving a genuinely signed
// document means these tests exercise the same parsing and threshold
// verification a client does, so a change that broke verification could not
// pass here while looking fine.
// --------------------------------------------------------------------------

func pinFor(seed string) string {
	// A pin is "sha256/" plus 44 base64 characters. The value only has to be
	// well-formed and distinct per relay; nothing here completes a handshake.
	return "sha256/" + strings.Repeat(seed, 43)[:43] + "="
}

func descriptor(t *testing.T, nickname, addr string) *directory.Descriptor {
	t.Helper()
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	now := time.Now()
	d := &directory.Descriptor{
		Nickname:     nickname,
		Address:      addr,
		TLSPin:       pinFor(string(nickname[0])),
		Identity:     id.Fingerprint(),
		Destinations: []string{dest},
		Published:    now.Add(-time.Minute),
		Expires:      now.Add(24 * time.Hour),
	}
	encoded, err := d.Sign(id)
	if err != nil {
		t.Fatalf("signing descriptor %s: %v", nickname, err)
	}
	// Parse it back, so the descriptor carries the exact bytes that were
	// signed -- the same thing a consensus would hold.
	parsed, err := directory.ParseDescriptor(encoded)
	if err != nil {
		t.Fatalf("re-parsing descriptor %s: %v", nickname, err)
	}
	return parsed
}

// directoryServer serves a signed consensus over the relays named. The
// returned setter swaps the document, so a test can watch a refresh land;
// passing nil takes the authority down entirely.
//
// The setter is deliberately not variadic: set(nil) has to mean "serve
// nothing", and a variadic parameter would quietly turn it into a slice
// holding one nil descriptor.
func directoryServer(t *testing.T, relays ...*directory.Descriptor) (url string, authorities map[string]ed25519.PublicKey, set func([]*directory.Descriptor), hits *int64) {
	t.Helper()

	auth, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	var mu sync.Mutex
	var body []byte
	var count int64

	encode := func(rs []*directory.Descriptor) []byte {
		now := time.Now()
		c := &directory.Consensus{
			ValidAfter: now.Add(-time.Minute),
			ValidUntil: now.Add(time.Hour),
			Relays:     rs,
		}
		if err := c.Sign(auth); err != nil {
			t.Fatalf("signing consensus: %v", err)
		}
		b, err := c.Encode()
		if err != nil {
			t.Fatalf("encoding consensus: %v", err)
		}
		return b
	}

	body = encode(relays)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		count++
		if body == nil {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	set = func(rs []*directory.Descriptor) {
		mu.Lock()
		defer mu.Unlock()
		if rs == nil {
			body = nil // simulate an authority that has fallen over
			return
		}
		body = encode(rs)
	}

	return srv.URL, map[string]ed25519.PublicKey{auth.Fingerprint(): auth.Public}, set, &count
}

// --------------------------------------------------------------------------
// a dialer whose behaviour a test controls
// --------------------------------------------------------------------------

type fakeDialer struct {
	addr string
	mu   *sync.Mutex
	// errs maps a relay address to the error it should return; absent means
	// the dial succeeds.
	errs  map[string]error
	calls *[]string
}

func (f *fakeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	*f.calls = append(*f.calls, f.addr)
	err := f.errs[f.addr]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c1, c2 := net.Pipe()
	go func() { io.Copy(io.Discard, c2); c2.Close() }()
	return c1, nil
}

// harness wires a pool to a directory server and a controllable dialer.
type harness struct {
	pool  *Pool
	set   func([]*directory.Descriptor)
	mu    sync.Mutex
	errs  map[string]error
	calls []string
}

func (h *harness) fail(addr string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs[addr] = err
}

func (h *harness) heal(addr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.errs, addr)
}

func (h *harness) dialed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func (h *harness) resetCalls() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = nil
}

func newHarness(t *testing.T, relays ...*directory.Descriptor) *harness {
	t.Helper()
	url, authorities, set, _ := directoryServer(t, relays...)

	h := &harness{set: set, errs: map[string]error{}}
	p, err := New(Config{
		Fetcher:     &directory.Fetcher{URLs: []string{url}, Authorities: authorities, Threshold: 1},
		Destination: dest,
		Secret:      "s3cret",
		Logger:      quiet(),
		newDialer: func(addr, pin, secret string, timeout time.Duration) (Dialer, error) {
			return &fakeDialer{addr: addr, mu: &h.mu, errs: h.errs, calls: &h.calls}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.pool = p
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return h
}

func mustDial(t *testing.T, p *Pool) net.Conn {
	t.Helper()
	c, err := p.DialContext(context.Background(), "tcp", dest)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	return c
}

// --------------------------------------------------------------------------
// selection is sticky
// --------------------------------------------------------------------------

func TestSelectionIsSticky(t *testing.T) {
	h := newHarness(t,
		descriptor(t, "alpha", "10.0.0.1:8443"),
		descriptor(t, "bravo", "10.0.0.2:8443"),
		descriptor(t, "charlie", "10.0.0.3:8443"),
	)

	for i := 0; i < 12; i++ {
		mustDial(t, h.pool).Close()
	}

	calls := h.dialed()
	first := calls[0]
	for i, c := range calls {
		if c != first {
			t.Fatalf("dial %d went to %s, but %s was already chosen. "+
				"Rotating relays is what the guard design exists to prevent: "+
				"it spreads the same knowledge across more parties instead of dividing it", i, c, first)
		}
	}
	if _, addr, ok := h.pool.Current(); !ok || addr != first {
		t.Fatalf("Current() = %q, %v; want %q, true", addr, ok, first)
	}
}

// --------------------------------------------------------------------------
// failover
// --------------------------------------------------------------------------

func TestFailoverWhenTheGuardStopsAnswering(t *testing.T) {
	h := newHarness(t,
		descriptor(t, "alpha", "10.0.0.1:8443"),
		descriptor(t, "bravo", "10.0.0.2:8443"),
	)

	mustDial(t, h.pool).Close()
	_, guard, _ := h.pool.Current()

	// The chosen relay goes away.
	h.fail(guard, errors.New("connect: connection refused"))
	h.resetCalls()

	c := mustDial(t, h.pool)
	c.Close()

	_, now, ok := h.pool.Current()
	if !ok || now == guard {
		t.Fatalf("still on %s after it started refusing connections; a client that cannot move is a client that needs restarting", guard)
	}
	if got := h.pool.Stats().Failovers; got != 1 {
		t.Fatalf("Failovers = %d, want 1", got)
	}

	// And the new relay is now the sticky one.
	h.resetCalls()
	mustDial(t, h.pool).Close()
	if calls := h.dialed(); len(calls) != 1 || calls[0] != now {
		t.Fatalf("after failover, dials went to %v; want a single dial to %s", calls, now)
	}
}

func TestFailedRelayIsSkippedUntilItsBackoffExpires(t *testing.T) {
	h := newHarness(t,
		descriptor(t, "alpha", "10.0.0.1:8443"),
		descriptor(t, "bravo", "10.0.0.2:8443"),
	)
	mustDial(t, h.pool).Close()
	_, guard, _ := h.pool.Current()

	h.fail(guard, errors.New("connect: connection refused"))
	mustDial(t, h.pool).Close() // fails over

	// The broken relay is healed, but it is still inside its backoff, so
	// nothing should go back to it yet.
	h.heal(guard)
	h.resetCalls()
	for i := 0; i < 5; i++ {
		mustDial(t, h.pool).Close()
	}
	for _, c := range h.dialed() {
		if c == guard {
			t.Fatal("dialled a relay that is still in backoff; a relay that just failed should be left alone for a while")
		}
	}
}

func TestEveryRelayDownStillAttemptsRatherThanRefusing(t *testing.T) {
	h := newHarness(t,
		descriptor(t, "alpha", "10.0.0.1:8443"),
		descriptor(t, "bravo", "10.0.0.2:8443"),
	)

	h.fail("10.0.0.1:8443", errors.New("connection refused"))
	h.fail("10.0.0.2:8443", errors.New("connection refused"))

	if _, err := h.pool.DialContext(context.Background(), "tcp", dest); err == nil {
		t.Fatal("expected an error when every relay is refusing")
	}

	// Both are now in backoff. A recovery must still be noticed rather than
	// waiting out a timer that nobody is watching.
	h.heal("10.0.0.2:8443")
	h.resetCalls()
	c, err := h.pool.DialContext(context.Background(), "tcp", dest)
	if err != nil {
		t.Fatalf("a relay recovered but the pool would not try it: %v", err)
	}
	c.Close()
}

func TestAllRelaysRejectingTheCredentialSaysSo(t *testing.T) {
	h := newHarness(t,
		descriptor(t, "alpha", "10.0.0.1:8443"),
		descriptor(t, "bravo", "10.0.0.2:8443"),
	)
	rejected := errors.New("relay 10.0.0.1:8443 rejected the credential (407). Check the secret")
	h.fail("10.0.0.1:8443", rejected)
	h.fail("10.0.0.2:8443", rejected)

	_, err := h.pool.DialContext(context.Background(), "tcp", dest)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "OSANWE_SECRET") {
		t.Fatalf("error was %q; when every relay returns 407 the secret is the problem, and the message should say that rather than listing four identical failures", err)
	}
}

func TestCancelledContextDoesNotPenaliseTheRelay(t *testing.T) {
	h := newHarness(t, descriptor(t, "alpha", "10.0.0.1:8443"))
	mustDial(t, h.pool).Close()

	h.fail("10.0.0.1:8443", context.Canceled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.pool.DialContext(ctx, "tcp", dest); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}

	if got := h.pool.Stats().DialErrors; got != 0 {
		t.Fatalf("DialErrors = %d, want 0: the caller gave up, which says nothing about the relay", got)
	}
}

// --------------------------------------------------------------------------
// refresh
// --------------------------------------------------------------------------

func TestRefreshFailureKeepsTheRelaysAlreadyHeld(t *testing.T) {
	h := newHarness(t, descriptor(t, "alpha", "10.0.0.1:8443"))
	mustDial(t, h.pool).Close()

	h.set(nil) // the authority falls over

	if err := h.pool.Refresh(context.Background()); err == nil {
		t.Fatal("expected the refresh to report an error")
	}
	if n := h.pool.Len(); n != 1 {
		t.Fatalf("relays held = %d, want 1: a directory outage must not become a client outage", n)
	}
	// And traffic still flows.
	mustDial(t, h.pool).Close()
}

func TestGuardSurvivesARefreshThatStillListsIt(t *testing.T) {
	alpha := descriptor(t, "alpha", "10.0.0.1:8443")
	bravo := descriptor(t, "bravo", "10.0.0.2:8443")
	h := newHarness(t, alpha, bravo)

	mustDial(t, h.pool).Close()
	_, guard, _ := h.pool.Current()

	h.set([]*directory.Descriptor{alpha, bravo})
	if err := h.pool.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, now, ok := h.pool.Current(); !ok || now != guard {
		t.Fatalf("guard moved from %s to %s across a refresh that still listed it; "+
			"rotating on every refresh is rotation with extra steps", guard, now)
	}
	if got := h.pool.Stats().Failovers; got != 0 {
		t.Fatalf("Failovers = %d, want 0", got)
	}
}

func TestGuardIsDroppedWhenItLeavesTheConsensus(t *testing.T) {
	alpha := descriptor(t, "alpha", "10.0.0.1:8443")
	bravo := descriptor(t, "bravo", "10.0.0.2:8443")
	h := newHarness(t, alpha, bravo)

	mustDial(t, h.pool).Close()
	_, guard, _ := h.pool.Current()

	// Publish a consensus without the relay in use.
	remaining := bravo
	if guard == bravo.Address {
		remaining = alpha
	}
	h.set([]*directory.Descriptor{remaining})
	if err := h.pool.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, _, ok := h.pool.Current(); ok {
		t.Fatal("kept using a relay the authorities stopped listing; withdrawal has to actually take effect")
	}
	h.resetCalls()
	mustDial(t, h.pool).Close()
	if calls := h.dialed(); len(calls) != 1 || calls[0] != remaining.Address {
		t.Fatalf("dialled %v; want a single dial to the relay still listed, %s", calls, remaining.Address)
	}
}

func TestRefreshCarriesFailureStateForward(t *testing.T) {
	alpha := descriptor(t, "alpha", "10.0.0.1:8443")
	bravo := descriptor(t, "bravo", "10.0.0.2:8443")
	h := newHarness(t, alpha, bravo)

	mustDial(t, h.pool).Close()
	_, guard, _ := h.pool.Current()
	h.fail(guard, errors.New("connection refused"))
	mustDial(t, h.pool).Close() // fail over, marking the guard down

	if err := h.pool.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// The relay is healthy again but still in backoff. If the refresh had
	// rebuilt the set from scratch it would have forgotten that, and traffic
	// would go straight back at a relay that has been failing.
	h.heal(guard)
	h.resetCalls()
	for i := 0; i < 4; i++ {
		mustDial(t, h.pool).Close()
	}
	for _, c := range h.dialed() {
		if c == guard {
			t.Fatal("a refresh erased the backoff on a failing relay")
		}
	}
}

func TestRunRefreshesOnItsTimer(t *testing.T) {
	url, authorities, set, hits := directoryServer(t, descriptor(t, "alpha", "10.0.0.1:8443"))
	p, err := New(Config{
		Fetcher:      &directory.Fetcher{URLs: []string{url}, Authorities: authorities, Threshold: 1},
		Destination:  dest,
		Secret:       "s",
		RefreshEvery: 20 * time.Millisecond,
		Logger:       quiet(),
		newDialer: func(addr, pin, secret string, timeout time.Duration) (Dialer, error) {
			return &fakeDialer{addr: addr, mu: new(sync.Mutex), errs: map[string]error{}, calls: new([]string)}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	_ = set

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.Stats().Refreshes >= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Run did not refresh on its timer: %d refreshes, %d directory hits", p.Stats().Refreshes, *hits)
}

// --------------------------------------------------------------------------
// construction
// --------------------------------------------------------------------------

func TestNewRejectsAnIncompleteConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no fetcher", Config{Destination: dest}, "Fetcher is required"},
		{"no destination", Config{Fetcher: &directory.Fetcher{}}, "Destination is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestDialBeforeAnyRefreshFailsClearly(t *testing.T) {
	p, err := New(Config{
		Fetcher:     &directory.Fetcher{URLs: []string{"http://127.0.0.1:1"}, Authorities: map[string]ed25519.PublicKey{"x": make(ed25519.PublicKey, 32)}, Threshold: 1},
		Destination: dest,
		Logger:      quiet(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.DialContext(context.Background(), "tcp", dest)
	if err == nil || !strings.Contains(err.Error(), "no relays known") {
		t.Fatalf("error = %v, want one saying no relays are known yet", err)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if got, want := backoff(1), minBackoff; got != want {
		t.Fatalf("backoff(1) = %v, want %v", got, want)
	}
	if got, want := backoff(2), 2*minBackoff; got != want {
		t.Fatalf("backoff(2) = %v, want %v", got, want)
	}
	for _, n := range []int{20, 50, 1000} {
		if got := backoff(n); got != maxBackoff {
			t.Fatalf("backoff(%d) = %v, want the cap %v: an uncapped backoff shuns a relay long after whatever broke it is fixed", n, got, maxBackoff)
		}
	}
}

func TestClientsDoNotAllStartOnTheSameRelay(t *testing.T) {
	// The consensus has a fixed order. If the pool simply took the first entry
	// every client in the world would land on one relay -- bad for load, and
	// worse for privacy, since that relay would see everybody's timing.
	relays := []*directory.Descriptor{
		descriptor(t, "alpha", "10.0.0.1:8443"),
		descriptor(t, "bravo", "10.0.0.2:8443"),
		descriptor(t, "charlie", "10.0.0.3:8443"),
		descriptor(t, "delta", "10.0.0.4:8443"),
		descriptor(t, "echo", "10.0.0.5:8443"),
	}

	seen := map[string]int{}
	for i := 0; i < 25; i++ {
		h := newHarness(t, relays...)
		mustDial(t, h.pool).Close()
		_, addr, ok := h.pool.Current()
		if !ok {
			t.Fatal("no relay chosen")
		}
		seen[addr]++
	}

	if len(seen) < 2 {
		t.Fatalf("25 independent clients all chose %v; initial selection is not being randomised", seen)
	}
}
