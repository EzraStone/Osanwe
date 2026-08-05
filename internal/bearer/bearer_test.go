package bearer

import (
	"bufio"
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// directDialer connects straight to an address, standing in for a tunnel so
// these tests exercise bearer rather than the relay.
type directDialer struct {
	calls   chan string
	replace string // if set, every dial goes here regardless of the requested address
}

func (d *directDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.calls != nil {
		select {
		case d.calls <- addr:
		default:
		}
	}
	target := addr
	if d.replace != "" {
		target = d.replace
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}

type failingDialer struct{}

func (failingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("no relay available")
}

// upstreamTLS starts an HTTPS server whose certificate the returned pool trusts.
func upstreamTLS(t *testing.T, h http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, pool
}

// startBearer wires a Server whose transport trusts the test upstream.
func startBearer(t *testing.T, cfg Config, pool *x509.CertPool, sni string) *Server {
	t.Helper()
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Trust the test certificate. Production verification is unchanged; this
	// only teaches the transport about httptest's throwaway CA. httptest's
	// certificate carries the name example.com, so SNI must match it.
	s.tr.TLSClientConfig.RootCAs = pool
	s.tr.TLSClientConfig.ServerName = "example.com"
	_ = sni

	if err := s.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}

func TestNewRejectsNonLoopbackBinds(t *testing.T) {
	d := &directDialer{}
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.0.2.10:8080", "[::]:8080"} {
		if _, err := New(Config{Addr: addr, Dialer: d}); err == nil {
			t.Errorf("New(%q) succeeded; binding a routable address would put prompts on the network in plaintext", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		if _, err := New(Config{Addr: addr, Dialer: d}); err != nil {
			t.Errorf("New(%q) failed for a loopback address: %v", addr, err)
		}
	}
	// The escape hatch must exist for callers who know what they are doing.
	if _, err := New(Config{Addr: "0.0.0.0:8080", Dialer: d, AllowNonLoopback: true}); err != nil {
		t.Errorf("AllowNonLoopback did not permit a routable bind: %v", err)
	}
}

func TestNewRejectsPlaintextUpstream(t *testing.T) {
	d := &directDialer{}
	for _, up := range []string{"http://api.anthropic.com", "http://localhost:8080"} {
		if _, err := New(Config{Addr: "127.0.0.1:0", Dialer: d, Upstream: up}); err == nil {
			t.Errorf("New accepted plaintext upstream %q; the relay would be able to read every prompt", up)
		}
	}
	if _, err := New(Config{Addr: "127.0.0.1:0", Dialer: d, Upstream: "https://"}); err == nil {
		t.Error("New accepted an upstream with no host")
	}
}

func TestNewRequiresDialer(t *testing.T) {
	if _, err := New(Config{Addr: "127.0.0.1:0"}); err == nil {
		t.Error("New succeeded without a Dialer")
	}
}

func TestForwardsRequestAndResponse(t *testing.T) {
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "sk-test" {
			t.Errorf("X-Api-Key = %q, want sk-test; the caller's key must pass through", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))

	d := &directDialer{calls: make(chan string, 4), replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://api.anthropic.com"}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest("POST", "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}

	// The tunnel must have been asked for the provider's address, not the
	// rewritten test address.
	select {
	case addr := <-d.calls:
		if addr != "api.anthropic.com:443" {
			t.Errorf("dialled %q, want api.anthropic.com:443", addr)
		}
	default:
		t.Error("dialer was never called; the request did not go through the tunnel")
	}
}

func TestDoesNotLeakForwardedHeaders(t *testing.T) {
	seen := make(chan http.Header, 1)
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		fmt.Fprint(w, "{}")
	}))

	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest("POST", "http://"+s.Addr().String()+"/v1/messages", strings.NewReader("{}"))
	// A hostile or careless local tool might set these itself.
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("Forwarded", "for=203.0.113.9")
	req.Header.Set("Proxy-Authorization", "Bearer relay-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	h := <-seen
	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if v := h.Get(name); v != "" {
			t.Errorf("%s reached the provider as %q; it must be stripped", name, v)
		}
	}
	if v := h.Get("Proxy-Authorization"); v != "" {
		t.Errorf("Proxy-Authorization reached the provider as %q; the relay credential must never be forwarded", v)
	}
}

func TestStreamsIncrementally(t *testing.T) {
	// A streaming response must arrive in pieces. Buffering would turn token
	// streaming into one long pause followed by a wall of text.
	release := make(chan struct{})
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: first\n\n")
		fl.Flush()
		<-release
		fmt.Fprint(w, "data: second\n\n")
		fl.Flush()
	}))

	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d}, pool, up.Listener.Addr().String())

	resp, err := http.Get("http://" + s.Addr().String() + "/v1/messages")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	done := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		done <- line
	}()

	select {
	case line := <-done:
		if !strings.Contains(line, "first") {
			t.Fatalf("first chunk = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first chunk did not arrive before the second was written; the response is being buffered")
	}

	close(release)
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), "second") {
		t.Errorf("second chunk missing from %q", rest)
	}
}

func TestTunnelFailureBecomesReadable502(t *testing.T) {
	s, err := New(Config{Addr: "127.0.0.1:0", Dialer: failingDialer{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	resp, err := http.Get("http://" + s.Addr().String() + "/v1/messages")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// The error must be JSON, since the caller is an API client that will try
	// to parse whatever it gets.
	if !strings.Contains(string(body), "osanwe_tunnel_error") {
		t.Errorf("body = %q, want a JSON error a client can parse", body)
	}
	if s.Metrics().TunnelFails.Load() != 1 {
		t.Errorf("TunnelFails = %d, want 1", s.Metrics().TunnelFails.Load())
	}
}

func TestUpstreamAddr(t *testing.T) {
	d := &directDialer{}
	for up, want := range map[string]string{
		"https://api.anthropic.com":      "api.anthropic.com:443",
		"https://api.anthropic.com:8443": "api.anthropic.com:8443",
	} {
		s, err := New(Config{Addr: "127.0.0.1:0", Dialer: d, Upstream: up})
		if err != nil {
			t.Fatalf("New(%q): %v", up, err)
		}
		if got := s.UpstreamAddr(); got != want {
			t.Errorf("UpstreamAddr() for %q = %q, want %q", up, got, want)
		}
	}
}

func TestBasePathIsPreserved(t *testing.T) {
	seen := make(chan string, 1)
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path
		fmt.Fprint(w, "{}")
	}))

	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://gateway.example/api"}, pool, up.Listener.Addr().String())

	resp, err := http.Get("http://" + s.Addr().String() + "/v1/messages")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if got := <-seen; got != "/api/v1/messages" {
		t.Errorf("upstream path = %q, want /api/v1/messages", got)
	}
}
