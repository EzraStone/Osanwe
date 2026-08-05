package directory

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveBytes(t *testing.T, body []byte, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchVerifiesBeforeReturning(t *testing.T) {
	ids, set := authorities(t, 2)
	data := buildConsensus(t, ids, relay(t, "alpha"))
	srv := serveBytes(t, data, 200)

	f := &Fetcher{URLs: []string{srv.URL}, Authorities: set, Threshold: 2}
	c, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(c.Relays) != 1 || c.Relays[0].Nickname != "alpha" {
		t.Errorf("unexpected relays: %v", names(c.Relays))
	}
}

func TestFetchRejectsUnderSignedConsensus(t *testing.T) {
	ids, set := authorities(t, 3)
	data := buildConsensus(t, ids[:1], relay(t, "alpha"))
	srv := serveBytes(t, data, 200)

	f := &Fetcher{URLs: []string{srv.URL}, Authorities: set, Threshold: 2}
	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch returned a consensus that did not meet the threshold")
	}
}

func TestFetchRejectsHostileDirectory(t *testing.T) {
	_, set := authorities(t, 2)

	// A directory that signs with its own keys, which the client does not trust.
	hostile, _ := authorities(t, 2)
	data := buildConsensus(t, hostile, relay(t, "attacker"))
	srv := serveBytes(t, data, 200)

	f := &Fetcher{URLs: []string{srv.URL}, Authorities: set, Threshold: 1}
	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch accepted a consensus signed entirely by untrusted keys")
	}
}

func TestFetchRequiresConfiguration(t *testing.T) {
	ids, set := authorities(t, 1)
	srv := serveBytes(t, buildConsensus(t, ids, relay(t, "alpha")), 200)

	cases := map[string]*Fetcher{
		"no URLs":        {Authorities: set, Threshold: 1},
		"no authorities": {URLs: []string{srv.URL}, Threshold: 1},
		"zero threshold": {URLs: []string{srv.URL}, Authorities: set, Threshold: 0},
	}
	for name, f := range cases {
		if _, err := f.Fetch(context.Background()); err == nil {
			t.Errorf("%s: Fetch succeeded; an unconfigured fetcher must not accept documents", name)
		}
	}
}

func TestFetchFallsBackToAnotherEndpoint(t *testing.T) {
	ids, set := authorities(t, 1)
	good := serveBytes(t, buildConsensus(t, ids, relay(t, "alpha")), 200)
	broken := serveBytes(t, []byte("not a consensus"), 200)
	down := serveBytes(t, nil, 500)

	f := &Fetcher{
		URLs:        []string{broken.URL, down.URL, good.URL},
		Authorities: set,
		Threshold:   1,
	}
	// Endpoints are tried in random order, so run enough times that a fallback
	// path is exercised whichever ordering comes up.
	for i := 0; i < 20; i++ {
		c, err := f.Fetch(context.Background())
		if err != nil {
			t.Fatalf("attempt %d: Fetch failed despite one good endpoint: %v", i, err)
		}
		if c.Relays[0].Nickname != "alpha" {
			t.Fatalf("attempt %d: wrong relay %q", i, c.Relays[0].Nickname)
		}
	}
}

func TestFetchFailsWhenEveryEndpointFails(t *testing.T) {
	_, set := authorities(t, 1)
	a := serveBytes(t, []byte("garbage"), 200)
	b := serveBytes(t, nil, 404)

	f := &Fetcher{URLs: []string{a.URL, b.URL}, Authorities: set, Threshold: 1}
	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch succeeded with no working endpoint")
	}
}

func TestFetchBoundsResponseSize(t *testing.T) {
	_, set := authorities(t, 1)

	var served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An endless body. Without a limit this would consume memory until the
		// client died.
		chunk := make([]byte, 64<<10)
		for {
			n, err := w.Write(chunk)
			served.Add(int64(n))
			if err != nil || served.Load() > MaxConsensusSize*2 {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	f := &Fetcher{URLs: []string{srv.URL}, Authorities: set, Threshold: 1,
		HTTPClient: &http.Client{Timeout: 20 * time.Second}}

	done := make(chan error, 1)
	go func() { _, err := f.Fetch(context.Background()); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Fetch accepted an unbounded response")
		}
		if !strings.Contains(err.Error(), "larger than") {
			t.Logf("rejected for another reason, acceptable: %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("Fetch did not terminate on an endless body")
	}
}

func TestFetchHonoursContextCancellation(t *testing.T) {
	_, set := authorities(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	f := &Fetcher{URLs: []string{srv.URL}, Authorities: set, Threshold: 1}
	if _, err := f.Fetch(ctx); err == nil {
		t.Fatal("Fetch ignored a cancelled context")
	}
}

func TestSelectSpreadsAcrossRelays(t *testing.T) {
	var relays []*Descriptor
	for i := 0; i < 5; i++ {
		relays = append(relays, relay(t, fmt.Sprintf("r%d", i)))
	}

	// Always picking the first entry would concentrate every client on one
	// relay, which is bad for load and worse for privacy.
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		d, err := Select(relays)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		seen[d.Nickname] = true
	}
	if len(seen) < 2 {
		t.Errorf("Select returned only %v across 200 draws; selection is not spreading load", seen)
	}
}

func TestSelectRejectsEmptyCandidates(t *testing.T) {
	if _, err := Select(nil); err == nil {
		t.Fatal("Select succeeded with no candidates")
	}
}

func TestFetchUsesPlainHTTPSafely(t *testing.T) {
	// Transport security is deliberately not relied upon: the document is
	// signed, so a plain-HTTP mirror is fine, and a hostile authority over
	// HTTPS would still be rejected. This pins that reasoning.
	ids, set := authorities(t, 1)
	srv := serveBytes(t, buildConsensus(t, ids, relay(t, "alpha")), 200)
	if !strings.HasPrefix(srv.URL, "http://") {
		t.Fatalf("expected a plain-HTTP test server, got %q", srv.URL)
	}

	f := &Fetcher{URLs: []string{srv.URL}, Authorities: set, Threshold: 1}
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch over plain HTTP failed: %v", err)
	}

	// And the same endpoint serving a document signed by strangers is refused.
	hostile, _ := authorities(t, 1)
	bad := serveBytes(t, buildConsensus(t, hostile, relay(t, "attacker")), 200)
	f2 := &Fetcher{URLs: []string{bad.URL}, Authorities: set, Threshold: 1}
	if _, err := f2.Fetch(context.Background()); err == nil {
		t.Fatal("accepted a consensus from an untrusted signer")
	}
}

var _ = ed25519.PublicKey(nil)
