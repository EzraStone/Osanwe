// Package health checks that a relay is actually reachable and is presenting
// the key its descriptor claims.
//
// A directory that lists relays purely because their descriptors parse will
// keep advertising a machine that has been off for a week, and clients will
// keep selecting it. Worse, it will keep advertising a pin that no longer
// matches the relay's certificate, which looks to a client exactly like an
// impersonation attempt.
//
// The probe is a TLS handshake and nothing more. It needs no credential,
// because a relay presents its certificate before asking who is calling, and
// it deliberately stops short of authenticating: an authority has no business
// holding relay secrets, and a probe that could open a tunnel would be a probe
// that could carry traffic.
package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/EzraStone/osanwe/internal/certs"
)

// DefaultTimeout bounds a single probe.
const DefaultTimeout = 10 * time.Second

// DefaultConcurrency bounds how many relays are probed at once, so a directory
// listing many relays does not open hundreds of sockets in a burst.
const DefaultConcurrency = 8

// Result describes one probe.
type Result struct {
	Address string
	OK      bool
	Err     error

	// ObservedPin is what the relay actually presented, filled in even on a
	// mismatch so an operator can see both values rather than being told only
	// that something was wrong.
	ObservedPin string
}

// Checker probes relays.
type Checker struct {
	Timeout     time.Duration
	Concurrency int

	// Dial is overridable for tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Probe connects to addr and reports whether the relay presented wantPin.
//
// A mismatch is treated as failure rather than as a fresh observation. The
// authority must never quietly re-publish a key it just discovered: a relay
// whose key changed either rotated it, in which case the operator republishes
// a descriptor and signs it, or it is being impersonated. Both cases are for
// the operator to resolve, not for a directory to paper over.
func (c *Checker) Probe(ctx context.Context, addr, wantPin string) Result {
	res := Result{Address: addr}

	expected, err := certs.NormalizePin(wantPin)
	if err != nil {
		res.Err = err
		return res
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}

	raw, err := dial(ctx, "tcp", addr)
	if err != nil {
		res.Err = fmt.Errorf("unreachable: %w", err)
		return res
	}
	defer raw.Close()

	var observed string
	conn := tls.Client(raw, &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The pin is the whole check, so chain and name verification are
		// skipped here exactly as a real client skips them.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("relay presented no certificate")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("parsing relay certificate: %w", err)
			}
			observed = certs.Pin(leaf)
			if observed != expected {
				return fmt.Errorf("key mismatch")
			}
			return nil
		},
	})
	defer conn.Close()

	if err := conn.HandshakeContext(ctx); err != nil {
		res.ObservedPin = observed
		if observed != "" && observed != expected {
			res.Err = fmt.Errorf("relay is presenting %s but its descriptor claims %s; "+
				"the operator either rotated the certificate without republishing, or something is impersonating the relay",
				observed, expected)
			return res
		}
		res.Err = fmt.Errorf("handshake failed: %w", err)
		return res
	}

	res.ObservedPin = observed
	res.OK = true
	return res
}

// Target is one relay to probe.
type Target struct {
	Key     string // caller's identifier, echoed back in the result map
	Address string
	Pin     string
}

// ProbeAll probes targets concurrently and returns results keyed by Target.Key.
func (c *Checker) ProbeAll(ctx context.Context, targets []Target) map[string]Result {
	limit := c.Concurrency
	if limit <= 0 {
		limit = DefaultConcurrency
	}

	results := make(map[string]Result, len(targets))
	var mu sync.Mutex
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for _, t := range targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r := c.Probe(ctx, t.Address, t.Pin)
			mu.Lock()
			results[t.Key] = r
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return results
}

// Tracker remembers consecutive failures so a relay is not dropped from the
// directory because of one bad moment.
//
// Networks are unreliable and a directory that removed a relay the first time
// a probe timed out would flap constantly, taking clients with it. Equally, a
// relay that has been down for hours should not stay listed. The threshold is
// the compromise, and it is the caller's to choose.
type Tracker struct {
	mu       sync.Mutex
	failures map[string]int
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker { return &Tracker{failures: map[string]int{}} }

// Record notes a probe outcome and returns the current consecutive-failure
// count. A success resets the count to zero.
func (t *Tracker) Record(key string, ok bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok {
		delete(t.failures, key)
		return 0
	}
	t.failures[key]++
	return t.failures[key]
}

// Failures reports the current consecutive-failure count.
func (t *Tracker) Failures(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failures[key]
}

// Forget drops tracking for a relay that is no longer known, so the map does
// not grow without bound as relays come and go.
func (t *Tracker) Forget(keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.failures {
		if !keep[k] {
			delete(t.failures, k)
		}
	}
}
