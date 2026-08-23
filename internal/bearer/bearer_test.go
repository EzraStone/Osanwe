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
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

type trackingWallet struct {
	mu    sync.Mutex
	taken int
	put   int
}

func (w *trackingWallet) Take(context.Context) (*mint.Token, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.taken++
	return &mint.Token{KeyID: "mint-test", Nonce: []byte("nonce"), Sig: []byte("signature")}, nil
}

func (w *trackingWallet) Put(*mint.Token) {
	w.mu.Lock()
	w.put++
	w.mu.Unlock()
}

func (w *trackingWallet) counts() (taken, put int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.taken, w.put
}

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

// stopBearer waits for request handlers to unwind. A client can observe the
// final response byte just before withToken records a refund, so accounting
// assertions must not race that final handler bookkeeping.
func stopBearer(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
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
	for _, up := range []string{
		"https://user:pass@api.anthropic.com",
		"https://api.anthropic.com?caller=alice",
		"https://api.anthropic.com/#fragment",
		"https://api.anthropic.com/api%2Fv1",
	} {
		if _, err := New(Config{Addr: "127.0.0.1:0", Dialer: d, Upstream: up}); err == nil {
			t.Errorf("New accepted ambiguous upstream %q", up)
		}
	}
}

func TestNewRequiresDialer(t *testing.T) {
	if _, err := New(Config{Addr: "127.0.0.1:0"}); err == nil {
		t.Error("New succeeded without a Dialer")
	}
}

func TestTokenModeRequiresAnExplicitGatewayUpstream(t *testing.T) {
	for _, upstream := range []string{"", " \t\n"} {
		_, err := New(Config{
			Addr: "127.0.0.1:0", Dialer: &directDialer{},
			Upstream: upstream, Tokens: &trackingWallet{},
		})
		if err == nil || !strings.Contains(err.Error(), "explicit gateway") {
			t.Errorf("New token mode with upstream %q error = %v; a paid token could be sent to the default provider", upstream, err)
		}
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
		if got := r.Header.Get("Authorization"); got != "Bearer sk-auth-test" {
			t.Errorf("Authorization = %q, want documented BYOK credential", got)
		}
		if got := r.Header.Get("Api-Key"); got != "sk-api-test" {
			t.Errorf("Api-Key = %q, want documented BYOK credential", got)
		}
		if got := r.Header.Get("X-Api-Secret"); got != "" {
			t.Errorf("undocumented credential-shaped header escaped allowlist: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))

	d := &directDialer{calls: make(chan string, 4), replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://api.anthropic.com"}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest("POST", "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "sk-test")
	req.Header.Set("Authorization", "Bearer sk-auth-test")
	req.Header.Set("Api-Key", "sk-api-test")
	req.Header.Set("X-Api-Secret", "must-not-pass")
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
	req.Header.Set("User-Agent", "caller-sdk/9.8.7")
	req.Header.Set("Traceparent", "00-caller-trace-id-caller-span-id-01")
	req.Header.Set("X-Request-Id", "stable-caller-request-id")
	req.Header.Set("X-Stainless-Lang", "ruby")
	req.Header.Set("Cookie", "account=caller")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
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
	for _, name := range []string{"User-Agent", "Traceparent", "X-Request-Id", "X-Stainless-Lang", "Cookie"} {
		if v := h.Get(name); v != "" {
			t.Errorf("%s reached the provider as %q", name, v)
		}
	}
	for name, want := range map[string]string{
		"Accept": "application/json", "Content-Type": "application/json", "Anthropic-Version": "2023-06-01",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("allowed API header %s = %q, want %q", name, got, want)
		}
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

func TestTokenModeRewriteClearsAlternateCallerURLRepresentations(t *testing.T) {
	s, err := New(Config{
		Addr: "127.0.0.1:0", Dialer: &directDialer{}, Upstream: "https://gateway.example/base", Tokens: &trackingWallet{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	inURL := &url.URL{
		Scheme: "http", Opaque: "//attacker.example/v1/files", User: url.UserPassword("u", "p"),
		Host: "attacker.example", Path: "/v1/messages", RawPath: "/v1/%6dessages", OmitHost: true,
		ForceQuery: true, RawQuery: "caller=alice", Fragment: "fragment", RawFragment: "raw-fragment",
	}
	in := &http.Request{Method: http.MethodPost, URL: inURL, Header: http.Header{}}
	out := in.Clone(context.Background())
	out.URL = new(url.URL)
	*out.URL = *inURL
	out.Trailer = http.Header{"X-Stable-Id": []string{"alice"}}
	out.TransferEncoding = []string{"chunked"}
	s.rewrite(&httputil.ProxyRequest{In: in, Out: out})

	if out.URL.Scheme != "https" || out.URL.Host != "gateway.example" || out.URL.Path != "/base/v1/messages" {
		t.Fatalf("rewritten URL = %#v", out.URL)
	}
	if out.URL.Opaque != "" || out.URL.User != nil || out.URL.RawPath != "" || out.URL.OmitHost || out.URL.ForceQuery ||
		out.URL.RawQuery != "" || out.URL.Fragment != "" || out.URL.RawFragment != "" {
		t.Fatalf("caller URL residue survived rewrite: %#v", out.URL)
	}
	if len(out.Trailer) != 0 || len(out.TransferEncoding) != 0 {
		t.Fatalf("trailer state survived rewrite: trailers=%v transfer_encoding=%v", out.Trailer, out.TransferEncoding)
	}
}

func TestTokenModeCanonicalizesAnEncodedMessagesPathOnTheWire(t *testing.T) {
	wallet := &trackingWallet{}
	requestURI := make(chan string, 1)
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI <- r.RequestURI
		w.Header().Set(gatewayTokenOutcomeHeader, tokenOutcomeSpent)
		fmt.Fprint(w, `{}`)
	}))
	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://gateway.example", Tokens: wallet}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/v1/%6dessages", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()
	if got := <-requestURI; got != "/v1/messages" {
		t.Fatalf("gateway RequestURI = %q, want canonical /v1/messages", got)
	}
}

func TestFreeModelCatalogDoesNotTakeAWalletToken(t *testing.T) {
	wallet := &trackingWallet{}
	seen := make(chan http.Header, 1)
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"demo"}]}`)
	}))

	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://gateway.example", Tokens: wallet}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest(http.MethodGet, "http://"+s.Addr().String()+"/v1/models", nil)
	// A configured SDK may attach its own key to every request. Token mode must
	// not let even the free catalog carry that account credential away.
	req.Header.Set("Authorization", "Bearer user-key")
	req.Header.Set("X-Api-Key", "user-x-api-key")
	req.Header.Set("Api-Key", "user-api-key")
	req.Header.Set("User-Agent", "caller-sdk/9.8.7")
	req.Header.Set("Traceparent", "00-caller-trace-id-caller-span-id-01")
	req.Header.Set("Accept", "application/json; caller-id=alice")
	req.Header.Set("Content-Type", "application/json; caller-id=alice")
	req.Header.Set("Anthropic-Version", "caller-specific-version")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()

	if taken, put := wallet.counts(); taken != 0 || put != 0 {
		t.Fatalf("wallet counts = taken %d, put %d; a free catalog must not touch the wallet", taken, put)
	}
	headers := <-seen
	if got := headers.Get(gatewayTokenHeader); got != "" {
		t.Fatalf("free catalog carried a token: %q", got)
	}
	for _, name := range []string{"Authorization", "X-Api-Key", "Api-Key"} {
		if got := headers.Get(name); got != "" {
			t.Fatalf("free catalog leaked caller credential %s: %q", name, got)
		}
	}
	for _, name := range []string{"User-Agent", "Traceparent"} {
		if got := headers.Get(name); got != "" {
			t.Fatalf("free catalog leaked %s: %q", name, got)
		}
	}
	if got := headers.Get("Accept"); got != canonicalAccept {
		t.Fatalf("free catalog Accept = %q, want canonical %q", got, canonicalAccept)
	}
	if got := headers.Get("Content-Type"); got != "" {
		t.Fatalf("free catalog carried caller Content-Type %q", got)
	}
	if got := headers.Get("Anthropic-Version"); got != "" {
		t.Fatalf("free catalog carried caller Anthropic-Version %q", got)
	}
}

func TestTokenModeNormalizesSemanticHeaderValues(t *testing.T) {
	wallet := &trackingWallet{}
	seen := make(chan http.Header, 1)
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set(gatewayTokenOutcomeHeader, tokenOutcomeSpent)
		fmt.Fprint(w, `{}`)
	}))
	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://gateway.example", Tokens: wallet}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Add("Accept", "application/json; caller-id=alice")
	req.Header.Add("Accept", "application/json; caller-id=bob")
	req.Header.Set("Content-Type", "application/json; caller-id=alice")
	req.Header.Add("Anthropic-Version", "caller-specific-version")
	req.Header.Add("Anthropic-Version", "another-caller-version")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()

	headers := <-seen
	for name, want := range map[string]string{
		"Accept": canonicalAccept, "Content-Type": canonicalContentType, "Anthropic-Version": canonicalAnthropicVersion,
	} {
		if got := headers.Values(name); len(got) != 1 || got[0] != want {
			t.Fatalf("%s values = %q, want exactly %q", name, got, want)
		}
	}
}

func TestChunkedTrailersCannotBypassTheTokenModeHeaderAllowlist(t *testing.T) {
	wallet := &trackingWallet{}
	seen := make(chan http.Header, 1)
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		seen <- r.Trailer.Clone()
		w.Header().Set(gatewayTokenOutcomeHeader, tokenOutcomeSpent)
		fmt.Fprint(w, `{}`)
	}))
	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://gateway.example", Tokens: wallet}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	req.ContentLength = -1
	req.Trailer = http.Header{
		"Authorization": {"Bearer trailer-credential"},
		"X-Api-Key":     {"trailer-api-key"},
		"X-Stable-Id":   {"caller-alice"},
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()
	if trailers := <-seen; len(trailers) != 0 {
		t.Fatalf("gateway received caller trailers: %v", trailers)
	}
	stopBearer(t, s)
	if taken, put := wallet.counts(); taken != 1 || put != 0 {
		t.Fatalf("wallet counts = taken %d, put %d; want one spent token", taken, put)
	}
}

func TestGatewayTokenOutcomesControlWalletAccounting(t *testing.T) {
	cases := []struct {
		name       string
		outcome    string
		wantPut    int
		statusCode int
	}{
		{name: "spent", outcome: tokenOutcomeSpent, wantPut: 0, statusCode: http.StatusOK},
		{name: "refunded", outcome: tokenOutcomeRefunded, wantPut: 1, statusCode: http.StatusBadGateway},
		{name: "rejected", outcome: tokenOutcomeRejected, wantPut: 1, statusCode: http.StatusUnprocessableEntity},
		{name: "invalid", outcome: tokenOutcomeInvalid, wantPut: 0, statusCode: http.StatusUnauthorized},
		{name: "missing fails closed", outcome: "", wantPut: 0, statusCode: http.StatusOK},
		{name: "unknown fails closed", outcome: "provider-made-this-up", wantPut: 0, statusCode: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wallet := &trackingWallet{}
			up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get(gatewayTokenHeader); got == "" {
					t.Error("paid request reached the gateway without a token")
				}
				if tc.outcome != "" {
					w.Header().Set(gatewayTokenOutcomeHeader, tc.outcome)
				}
				w.WriteHeader(tc.statusCode)
				fmt.Fprint(w, `{}`)
			}))
			d := &directDialer{replace: up.Listener.Addr().String()}
			s := startBearer(t, Config{Dialer: d, Upstream: "https://gateway.example", Tokens: wallet}, pool, up.Listener.Addr().String())

			req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Post: %v", err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			stopBearer(t, s)

			if taken, put := wallet.counts(); taken != 1 || put != tc.wantPut {
				t.Fatalf("wallet counts = taken %d, put %d; want taken 1, put %d", taken, put, tc.wantPut)
			}
		})
	}
}

func TestUnsupportedTokenModeRequestsAreRefusedBeforeTakingPayment(t *testing.T) {
	wallet := &trackingWallet{}
	s, err := New(Config{
		Addr: "127.0.0.1:0", Dialer: failingDialer{},
		Upstream: "https://gateway.example", Tokens: wallet,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1/messages", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/files", http.StatusNotFound},
		{http.MethodPost, "/v1/messages?admin=true", http.StatusBadRequest},
		{http.MethodPost, "/v1/messages?", http.StatusBadRequest},
		{http.MethodGet, "/v1/models?caller=alice", http.StatusBadRequest},
		{http.MethodGet, "/v1/models?", http.StatusBadRequest},
		{http.MethodDelete, "/v1/models", http.StatusNotFound},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "http://127.0.0.1:8080"+tc.path, nil)
		rec := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	if taken, put := wallet.counts(); taken != 0 || put != 0 {
		t.Fatalf("unsupported requests touched wallet: taken %d, put %d", taken, put)
	}
}

func TestPreWriteTunnelFailureReturnsTokenToWallet(t *testing.T) {
	wallet := &trackingWallet{}
	s := startBearer(t, Config{
		Dialer: failingDialer{}, Upstream: "https://gateway.example", Tokens: wallet,
	}, nil, "")

	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	stopBearer(t, s)
	if taken, put := wallet.counts(); taken != 1 || put != 1 {
		t.Fatalf("wallet counts = taken %d, put %d; a failure before any request write is provably unspent", taken, put)
	}
}

func TestPostWriteConnectionResetDoesNotReturnTokenToWallet(t *testing.T) {
	wallet := &trackingWallet{}
	presented := make(chan string, 1)
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented <- r.Header.Get(gatewayTokenHeader)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{
		Dialer: d, Upstream: "https://gateway.example", Tokens: wallet,
	}, pool, up.Listener.Addr().String())

	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 after upstream reset", resp.StatusCode)
	}
	if got := <-presented; got == "" {
		t.Fatal("gateway received the request without a token")
	}
	stopBearer(t, s)
	if taken, put := wallet.counts(); taken != 1 || put != 0 {
		t.Fatalf("wallet counts = taken %d, put %d; a reset after token presentation is ambiguous and must fail closed", taken, put)
	}
}

func TestHTTP2TokenPresentationIsAccountedAsSpent(t *testing.T) {
	wallet := &trackingWallet{}
	protocol := make(chan int, 1)
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol <- r.ProtoMajor
		if got := r.Header.Get(gatewayTokenHeader); got == "" {
			t.Error("HTTP/2 request reached gateway without a token")
		}
		w.Header().Set(gatewayTokenOutcomeHeader, tokenOutcomeSpent)
		fmt.Fprint(w, `{}`)
	}))
	up.EnableHTTP2 = true
	up.StartTLS()
	t.Cleanup(up.Close)
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())

	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{
		Dialer: d, Upstream: "https://gateway.example", Tokens: wallet,
	}, pool, up.Listener.Addr().String())
	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := <-protocol; got != 2 {
		t.Fatalf("gateway protocol major = %d, want HTTP/2", got)
	}
	stopBearer(t, s)
	if taken, put := wallet.counts(); taken != 1 || put != 0 {
		t.Fatalf("wallet counts = taken %d, put %d; an HTTP/2 request acknowledged as spent must stay spent", taken, put)
	}
}

func TestProviderCannotMakeLocalInferenceCacheable(t *testing.T) {
	up, pool := upstreamTLS(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Referrer-Policy", "unsafe-url")
		fmt.Fprint(w, `{}`)
	}))
	d := &directDialer{replace: up.Listener.Addr().String()}
	s := startBearer(t, Config{Dialer: d, Upstream: "https://provider.example"}, pool, up.Listener.Addr().String())

	resp, err := http.Get("http://" + s.Addr().String() + "/v1/messages")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
