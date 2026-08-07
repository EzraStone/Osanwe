package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/bearer"
	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/directory"
	"github.com/EzraStone/osanwe/internal/policy"
	"github.com/EzraStone/osanwe/internal/pool"
	"github.com/EzraStone/osanwe/internal/ranger"
)

// A running relay, and the handle needed to stop it mid-test.
type liveRelay struct {
	nickname string
	addr     string
	pin      string
	stop     func()
}

func startRanger(t *testing.T, nickname, destination string) *liveRelay {
	t.Helper()

	cert, pin, err := certs.SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("%s cert: %v", nickname, err)
	}
	allowlist, err := policy.Parse([]string{destination})
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	authenticator, err := auth.New(secret)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	r, err := ranger.New(ranger.Config{
		Addr:      "127.0.0.1:0",
		TLS:       &tls.Config{Certificates: []tls.Certificate{cert}},
		Allowlist: allowlist,
		Auth:      authenticator,
	})
	if err != nil {
		t.Fatalf("ranger.New: %v", err)
	}
	if err := r.Listen(); err != nil {
		t.Fatalf("%s listen: %v", nickname, err)
	}
	go func() { _ = r.Serve() }()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = r.Shutdown(ctx)
	}
	t.Cleanup(stop)

	return &liveRelay{nickname: nickname, addr: r.Addr().String(), pin: pin, stop: stop}
}

// signedDirectory serves a consensus listing the relays given, signed by one
// authority. It is the real document format, verified by the real parser: a
// client that would reject this in production rejects it here too.
func signedDirectory(t *testing.T, destination string, relays ...*liveRelay) (url string, authorities map[string]ed25519.PublicKey) {
	t.Helper()

	authority, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	var descs []*directory.Descriptor
	for _, r := range relays {
		id, err := directory.GenerateIdentity()
		if err != nil {
			t.Fatalf("GenerateIdentity: %v", err)
		}
		now := time.Now()
		d := &directory.Descriptor{
			Nickname:     r.nickname,
			Address:      r.addr,
			TLSPin:       r.pin,
			Identity:     id.Fingerprint(),
			Destinations: []string{destination},
			Published:    now.Add(-time.Minute),
			Expires:      now.Add(time.Hour),
		}
		encoded, err := d.Sign(id)
		if err != nil {
			t.Fatalf("signing %s: %v", r.nickname, err)
		}
		parsed, err := directory.ParseDescriptor(encoded)
		if err != nil {
			t.Fatalf("parsing %s: %v", r.nickname, err)
		}
		descs = append(descs, parsed)
	}

	now := time.Now()
	c := &directory.Consensus{
		ValidAfter: now.Add(-time.Minute),
		ValidUntil: now.Add(time.Hour),
		Relays:     descs,
	}
	if err := c.Sign(authority); err != nil {
		t.Fatalf("signing consensus: %v", err)
	}
	body, err := c.Encode()
	if err != nil {
		t.Fatalf("encoding consensus: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv.URL, map[string]ed25519.PublicKey{authority.Fingerprint(): authority.Public}
}

// TestClientSurvivesLosingTheRelayItIsUsing is the behaviour the directory was
// built for and, until now, did not actually deliver: bearer chose a relay once
// at startup and kept dialling it forever, so losing that relay meant losing
// the client until somebody restarted it.
//
// Nothing here is stubbed. Two real rangers, a real signed consensus, a real
// provider over TLS. One relay is shut down mid-test and the client has to keep
// working on its own.
func TestClientSurvivesLosingTheRelayItIsUsing(t *testing.T) {
	// --- provider ---------------------------------------------------------
	providerCert, _, err := certs.SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("provider cert: %v", err)
	}
	providerLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{providerCert},
	})
	if err != nil {
		t.Fatalf("provider listen: %v", err)
	}
	provider := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"ok":true}`)
	})}
	go func() { _ = provider.Serve(providerLn) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	})
	destination := providerLn.Addr().String()

	roots := x509.NewCertPool()
	roots.AddCert(providerCert.Leaf)

	// --- two relays and a directory that lists both -----------------------
	alpha := startRanger(t, "alpha", destination)
	bravo := startRanger(t, "bravo", destination)
	dirURL, authorities := signedDirectory(t, destination, alpha, bravo)

	relays, err := pool.New(pool.Config{
		Fetcher:     &directory.Fetcher{URLs: []string{dirURL}, Authorities: authorities, Threshold: 1},
		Destination: destination,
		Secret:      secret,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	if err := relays.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if relays.Len() != 2 {
		t.Fatalf("pool holds %d relays, want 2", relays.Len())
	}

	// --- bearer, dialling through the pool --------------------------------
	client, err := bearer.New(bearer.Config{
		Addr:            "127.0.0.1:0",
		Upstream:        "https://" + destination,
		Dialer:          relays,
		UpstreamRootCAs: roots,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("bearer.New: %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("bearer listen: %v", err)
	}
	go func() { _ = client.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Shutdown(ctx)
	})

	post := func() error {
		resp, err := http.Post("http://"+client.Addr().String()+"/v1/messages",
			"application/json", strings.NewReader(`{"prompt":"hello"}`))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
		}
		return nil
	}

	// --- a working request, so we know which relay is in use --------------
	if err := post(); err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	chosen, chosenAddr, ok := relays.Current()
	if !ok {
		t.Fatal("no relay recorded after a successful request")
	}

	// --- kill it ----------------------------------------------------------
	switch chosenAddr {
	case alpha.addr:
		alpha.stop()
	case bravo.addr:
		bravo.stop()
	default:
		t.Fatalf("pool reported an unknown relay %q", chosenAddr)
	}

	// The client gets no notification that a relay died. It finds out by
	// failing, which means the first request afterwards may still be handed a
	// connection that was already open and is now closed -- exactly what
	// happens in production. What must not happen is that it stays broken.
	//
	// A few attempts over a couple of seconds is the honest bar: recovery
	// without human intervention, not recovery with zero dropped requests.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = post(); lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("the client never recovered after losing relay %s: %v", chosen, lastErr)
	}

	nowUsing, nowAddr, _ := relays.Current()
	if nowAddr == chosenAddr {
		t.Fatalf("still reporting the dead relay %s as current", chosenAddr)
	}
	if relays.Stats().Failovers == 0 {
		t.Fatal("recovered without recording a failover")
	}
	t.Logf("moved from %s to %s without a restart", chosen, nowUsing)

	// And it stays on the new relay rather than drifting back.
	for i := 0; i < 3; i++ {
		if err := post(); err != nil {
			t.Fatalf("request %d after failover failed: %v", i, err)
		}
	}
	if _, addr, _ := relays.Current(); addr != nowAddr {
		t.Fatalf("relay moved again to %s with nothing wrong; selection should be sticky", addr)
	}
}

// TestClientFailsClearlyWhenEveryRelayIsGone checks the other side: when there
// is genuinely nowhere to go, the request must fail rather than quietly
// reaching the provider without a relay in between.
func TestClientFailsClearlyWhenEveryRelayIsGone(t *testing.T) {
	providerCert, _, err := certs.SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("provider cert: %v", err)
	}
	providerLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{providerCert},
	})
	if err != nil {
		t.Fatalf("provider listen: %v", err)
	}
	t.Cleanup(func() { providerLn.Close() })
	destination := providerLn.Addr().String()

	alpha := startRanger(t, "alpha", destination)
	bravo := startRanger(t, "bravo", destination)
	dirURL, authorities := signedDirectory(t, destination, alpha, bravo)

	relays, err := pool.New(pool.Config{
		Fetcher:     &directory.Fetcher{URLs: []string{dirURL}, Authorities: authorities, Threshold: 1},
		Destination: destination,
		Secret:      secret,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	if err := relays.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	alpha.stop()
	bravo.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := relays.DialContext(ctx, "tcp", destination)
	if err == nil {
		conn.Close()
		t.Fatal("dialled successfully with every relay stopped; failing closed is the whole point")
	}
	if !strings.Contains(err.Error(), "no relay could carry") {
		t.Fatalf("error was %q; it should say plainly that no relay could carry the connection", err)
	}
}
