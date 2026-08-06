package health

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/certs"
)

// tlsRelay starts a TLS listener that completes handshakes, standing in for a
// live ranger. It returns the address and the pin it presents.
func tlsRelay(t *testing.T) (string, string) {
	t.Helper()
	cert, pin, err := certs.SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), pin
}

func TestProbeAcceptsALiveRelay(t *testing.T) {
	addr, pin := tlsRelay(t)

	c := &Checker{}
	res := c.Probe(context.Background(), addr, pin)
	if !res.OK {
		t.Fatalf("Probe failed against a live relay: %v", res.Err)
	}
	if res.ObservedPin != pin {
		t.Errorf("ObservedPin = %q, want %q", res.ObservedPin, pin)
	}
}

func TestProbeRejectsAWrongPin(t *testing.T) {
	addr, actual := tlsRelay(t)
	_, other, err := certs.SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}

	c := &Checker{}
	res := c.Probe(context.Background(), addr, other)
	if res.OK {
		t.Fatal("Probe accepted a relay presenting a key its descriptor did not claim")
	}
	// Both values must appear, so an operator can see what changed rather than
	// only being told that something is wrong.
	msg := res.Err.Error()
	if !strings.Contains(msg, actual) || !strings.Contains(msg, other) {
		t.Errorf("error does not name both the observed and expected pins: %v", res.Err)
	}
	if res.ObservedPin != actual {
		t.Errorf("ObservedPin = %q, want the key actually presented %q", res.ObservedPin, actual)
	}
}

func TestProbeReportsUnreachable(t *testing.T) {
	// Bind then close, so the address is routable but dead.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	dead := ln.Addr().String()
	ln.Close()

	_, pin := tlsRelay(t)
	c := &Checker{Timeout: 2 * time.Second}
	res := c.Probe(context.Background(), dead, pin)
	if res.OK {
		t.Fatal("Probe reported a dead address as healthy")
	}
	if !strings.Contains(res.Err.Error(), "unreachable") {
		t.Errorf("error = %v, want it to say unreachable", res.Err)
	}
}

func TestProbeRejectsAPlaintextListener(t *testing.T) {
	// A listener that accepts TCP but never speaks TLS. The handshake must
	// fail rather than hang until the caller's patience runs out.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = c.Write([]byte("hello\n")) }(c)
		}
	}()

	_, pin := tlsRelay(t)
	c := &Checker{Timeout: 3 * time.Second}
	res := c.Probe(context.Background(), ln.Addr().String(), pin)
	if res.OK {
		t.Fatal("Probe accepted a listener that does not speak TLS")
	}
}

func TestProbeRejectsAMalformedPin(t *testing.T) {
	addr, _ := tlsRelay(t)
	c := &Checker{}
	if res := c.Probe(context.Background(), addr, "not-a-pin"); res.OK {
		t.Fatal("Probe accepted a malformed expected pin")
	}
}

func TestProbeHonoursTimeout(t *testing.T) {
	// A listener that accepts and then says nothing. Without a timeout the
	// probe would block a rebuild indefinitely.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without completing a handshake.
			go func(c net.Conn) { time.Sleep(30 * time.Second); c.Close() }(c)
		}
	}()

	_, pin := tlsRelay(t)
	c := &Checker{Timeout: 500 * time.Millisecond}

	start := time.Now()
	res := c.Probe(context.Background(), ln.Addr().String(), pin)
	elapsed := time.Since(start)

	if res.OK {
		t.Fatal("Probe succeeded against a silent listener")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Probe took %s despite a 500ms timeout", elapsed)
	}
}

func TestProbeAllRunsConcurrentlyAndKeysResults(t *testing.T) {
	addrA, pinA := tlsRelay(t)
	addrB, pinB := tlsRelay(t)

	c := &Checker{Concurrency: 4}
	results := c.ProbeAll(context.Background(), []Target{
		{Key: "alpha", Address: addrA, Pin: pinA},
		{Key: "beta", Address: addrB, Pin: pinB},
		{Key: "gamma", Address: addrA, Pin: pinB}, // live, wrong pin
	})

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if !results["alpha"].OK || !results["beta"].OK {
		t.Errorf("healthy relays reported unhealthy: %+v", results)
	}
	if results["gamma"].OK {
		t.Error("a relay presenting the wrong key was reported healthy")
	}
}

func TestProbeAllBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int64

	c := &Checker{
		Concurrency: 3,
		Timeout:     2 * time.Second,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(80 * time.Millisecond)
			inFlight.Add(-1)
			return nil, fmt.Errorf("no dial in this test")
		},
	}

	var targets []Target
	_, pin := tlsRelay(t)
	for i := 0; i < 12; i++ {
		targets = append(targets, Target{Key: fmt.Sprint(i), Address: "127.0.0.1:1", Pin: pin})
	}
	c.ProbeAll(context.Background(), targets)

	if peak.Load() > 3 {
		t.Errorf("peak concurrency was %d, want at most 3; a directory with many relays would open a burst of sockets", peak.Load())
	}
}

func TestTrackerToleratesTransientFailures(t *testing.T) {
	tr := NewTracker()

	// A directory that dropped a relay on the first timeout would flap
	// constantly and take clients with it.
	if n := tr.Record("alpha", false); n != 1 {
		t.Errorf("first failure counted as %d, want 1", n)
	}
	if n := tr.Record("alpha", false); n != 2 {
		t.Errorf("second failure counted as %d, want 2", n)
	}
	if n := tr.Record("alpha", true); n != 0 {
		t.Errorf("success left the count at %d, want 0", n)
	}
	if got := tr.Failures("alpha"); got != 0 {
		t.Errorf("Failures after success = %d, want 0", got)
	}
	// Counting must be per relay.
	tr.Record("beta", false)
	if tr.Failures("alpha") != 0 || tr.Failures("beta") != 1 {
		t.Error("failure counts leaked between relays")
	}
}

func TestTrackerForgetsUnknownRelays(t *testing.T) {
	tr := NewTracker()
	tr.Record("alpha", false)
	tr.Record("beta", false)
	tr.Record("gamma", false)

	tr.Forget(map[string]bool{"alpha": true})

	if tr.Failures("alpha") != 1 {
		t.Error("Forget dropped a relay it was told to keep")
	}
	if tr.Failures("beta") != 0 || tr.Failures("gamma") != 0 {
		t.Error("Forget kept relays that are no longer known; the map would grow without bound")
	}
}

func TestTrackerIsSafeUnderConcurrency(t *testing.T) {
	tr := NewTracker()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tr.Record(fmt.Sprint(i%5), i%2 == 0)
			tr.Failures(fmt.Sprint(i % 5))
		}(i)
	}
	wg.Wait()
}
