package gateway

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

const pooledKey = "sk-pooled-the-client-must-never-see-this"

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

func mintKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := mint.GenerateKey(mint.MinKeyBits)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seen records what the provider was actually sent, which is where most of
// these assertions land: the gateway's job is defined by what does and does
// not arrive on the far side.
type seen struct {
	mu      sync.Mutex
	headers http.Header
	body    string
	hits    int
}

func (s *seen) snapshot() (http.Header, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers.Clone(), s.body, s.hits
}

// harness is a gateway in front of a stand-in provider over TLS.
type harness struct {
	gw       *Server
	m        *mint.Mint
	spent    *mint.SpentSet
	provider *seen
	client   *http.Client
	url      string
}

func newHarness(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()

	obs := &seen{headers: http.Header{}}
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"ok":true}`)
		}
	}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		obs.mu.Lock()
		obs.headers = r.Header.Clone()
		obs.body = string(body)
		obs.hits++
		obs.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(provider.Close)

	m, err := mint.New(mintKey(t), mint.OpenAuthorizer{})
	if err != nil {
		t.Fatalf("mint.New: %v", err)
	}
	spent := mint.NewSpentSet()

	// The provider uses a self-signed certificate, so the gateway needs its
	// root. Only the test's provider is trusted here; there is no option
	// anywhere in the gateway to skip verification.
	roots := x509.NewCertPool()
	roots.AddCert(provider.Certificate())

	gw, err := New(Config{
		Addr:            "127.0.0.1:0",
		Upstream:        provider.URL,
		MintKeys:        map[string]*rsa.PublicKey{m.KeyID(): m.PublicKey()},
		Spent:           spent,
		UpstreamRootCAs: roots,
		Credential: Credential{
			Header: "x-api-key",
			Value:  pooledKey,
		},
		Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}

	front := httptest.NewServer(gw.Handler())
	t.Cleanup(front.Close)

	return &harness{gw: gw, m: m, spent: spent, provider: obs, client: front.Client(), url: front.URL}
}

func (h *harness) token(t *testing.T) *mint.Token {
	t.Helper()
	bl, err := mint.Blind(h.m.PublicKey())
	if err != nil {
		t.Fatalf("Blind: %v", err)
	}
	sig, err := h.m.Issue(context.Background(), nil, bl.Blinded)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	tok, err := bl.Unblind(sig)
	if err != nil {
		t.Fatalf("Unblind: %v", err)
	}
	return tok
}

func (h *harness) post(t *testing.T, tok *mint.Token, extra http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.url+"/v1/messages", strings.NewReader(`{"prompt":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if tok != nil {
		req.Header.Set(TokenHeader, tok.Encode())
	}
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// --------------------------------------------------------------------------
// the two properties that define this component
// --------------------------------------------------------------------------

// The client's token buys the request and must stop at the gateway. Forwarding
// it would hand the provider a bearer instrument and a per-request identifier.
func TestTokenNeverReachesTheProvider(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.token(t)

	resp := h.post(t, tok, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	headers, body, hits := h.provider.snapshot()
	if hits != 1 {
		t.Fatalf("provider saw %d requests, want 1", hits)
	}
	if got := headers.Get(TokenHeader); got != "" {
		t.Fatalf("the provider received %s: %q", TokenHeader, got)
	}

	encoded := tok.Encode()
	for name, values := range headers {
		for _, v := range values {
			if strings.Contains(v, encoded) {
				t.Fatalf("the token appears in header %s sent to the provider", name)
			}
		}
	}
	if strings.Contains(body, encoded) {
		t.Fatal("the token appears in the body sent to the provider")
	}
}

// The pooled credential is the gateway's, and a client that could read it
// would have stolen everyone's inference budget.
func TestPooledCredentialIsUsedAndNeverReturned(t *testing.T) {
	h := newHarness(t, nil)
	resp := h.post(t, h.token(t), nil)
	defer resp.Body.Close()

	headers, _, _ := h.provider.snapshot()
	if got := headers.Get("x-api-key"); got != pooledKey {
		t.Fatalf("provider saw x-api-key %q, want the pooled key", got)
	}

	// Nothing coming back to the client may contain it.
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), pooledKey) {
		t.Fatal("the pooled credential came back in the response body")
	}
	for name, values := range resp.Header {
		for _, v := range values {
			if strings.Contains(v, pooledKey) {
				t.Fatalf("the pooled credential came back in response header %s", name)
			}
		}
	}
}

// A client must not be able to substitute their own account, nor override the
// gateway's credential with one of their choosing.
func TestClientCredentialsAreStrippedNotForwarded(t *testing.T) {
	h := newHarness(t, nil)

	extra := http.Header{}
	extra.Set("Authorization", "Bearer sk-the-clients-own-account")
	extra.Set("X-Api-Key", "sk-attempted-override")
	extra.Set("Cookie", "session=identifying")
	extra.Set("X-Forwarded-For", "203.0.113.7")

	resp := h.post(t, h.token(t), extra)
	defer resp.Body.Close()

	headers, _, _ := h.provider.snapshot()
	if got := headers.Get("x-api-key"); got != pooledKey {
		t.Fatalf("x-api-key = %q; a client overrode the gateway's credential", got)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Forwarded-For", "X-Real-Ip"} {
		if got := headers.Get(h); got != "" {
			t.Fatalf("the provider received %s: %q, which identifies the caller", h, got)
		}
	}
}

// --------------------------------------------------------------------------
// payment
// --------------------------------------------------------------------------

func TestRequestWithoutATokenIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	resp := h.post(t, nil, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatal("an unpaid request reached the provider")
	}
}

func TestForgedTokenIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.token(t)
	tok.Sig[len(tok.Sig)-1] ^= 1

	resp := h.post(t, tok, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatal("a forged token reached the provider")
	}
}

func TestTokenFromAnUnknownMintIsRefused(t *testing.T) {
	h := newHarness(t, nil)

	other, err := mint.GenerateKey(mint.MinKeyBits)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	om, err := mint.New(other, mint.OpenAuthorizer{})
	if err != nil {
		t.Fatalf("mint.New: %v", err)
	}
	bl, _ := mint.Blind(om.PublicKey())
	sig, _ := om.Issue(context.Background(), nil, bl.Blinded)
	tok, err := bl.Unblind(sig)
	if err != nil {
		t.Fatalf("Unblind: %v", err)
	}

	resp := h.post(t, tok, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatal("a token from a mint this gateway does not accept reached the provider")
	}
}

func TestTokenCannotBeSpentTwice(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.token(t)

	first := h.post(t, tok, nil)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.StatusCode)
	}

	second := h.post(t, tok, nil)
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second status = %d, want 409", second.StatusCode)
	}
	if _, _, hits := h.provider.snapshot(); hits != 1 {
		t.Fatalf("provider saw %d requests, want 1: a replayed token bought a second one", hits)
	}
}

// The token is spent before the request is forwarded, so concurrent copies of
// the same token cannot each slip through while the others are in flight.
func TestConcurrentReplayBuysExactlyOneRequest(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		// Hold the request open, widening any window between spending and
		// forwarding.
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, `{"ok":true}`)
	})
	tok := h.token(t)

	const racers = 16
	var wg sync.WaitGroup
	codes := make([]int, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/messages", strings.NewReader(`{}`))
			req.Header.Set(TokenHeader, tok.Encode())
			resp, err := h.client.Do(req)
			if err != nil {
				codes[i] = -1
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			codes[i] = resp.StatusCode
		}(i)
	}
	close(start)
	wg.Wait()

	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d of %d concurrent replays succeeded, want exactly 1", ok, racers)
	}
	if _, _, hits := h.provider.snapshot(); hits != 1 {
		t.Fatalf("provider saw %d requests, want 1", hits)
	}
}

// --------------------------------------------------------------------------
// refunds
// --------------------------------------------------------------------------

func TestTokenIsRefundedWhenTheProviderFails(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream is having a bad day", http.StatusBadGateway)
	})
	tok := h.token(t)

	resp := h.post(t, tok, nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if h.spent.Len() != 0 {
		t.Fatal("the token stayed spent after the provider returned nothing usable")
	}
	if got := h.gw.Metrics().Refunded.Load(); got != 1 {
		t.Fatalf("Refunded = %d, want 1", got)
	}

	// And it can be used again, which is the point of refunding it.
	h.provider.mu.Lock()
	h.provider.hits = 0
	h.provider.mu.Unlock()

	retry := h.post(t, tok, nil)
	defer retry.Body.Close()
	if retry.StatusCode == http.StatusConflict {
		t.Fatal("a refunded token was refused as already spent")
	}
}

// A provider that answers normally consumes the token, even if the answer is a
// refusal. A 400 is the provider doing its job, and the work was done.
func TestTokenIsNotRefundedOnAProviderRefusal(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "that request was malformed", http.StatusBadRequest)
	})
	tok := h.token(t)

	resp := h.post(t, tok, nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if h.spent.Len() != 1 {
		t.Fatalf("spent set holds %d, want 1: a 4xx is an answer, not a failure to answer", h.spent.Len())
	}
}

// --------------------------------------------------------------------------
// streaming
// --------------------------------------------------------------------------

// Buffering anywhere on this path would turn token streaming into one long
// pause followed by a wall of text.
func TestStreamingIsNotBuffered(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: first\n\n")
		flusher.Flush()
		<-release
		fmt.Fprint(w, "data: second\n\n")
		flusher.Flush()
	})

	resp := h.post(t, h.token(t), nil)
	defer resp.Body.Close()

	buf := make([]byte, 64)
	done := make(chan int, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		done <- n
	}()

	select {
	case n := <-done:
		if !strings.Contains(string(buf[:n]), "first") {
			t.Fatalf("first read returned %q, want the first event", buf[:n])
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the first event never arrived; the response is being buffered until the stream ends")
	}
	close(release)
}

// --------------------------------------------------------------------------
// construction
// --------------------------------------------------------------------------

func TestNewRefusesAnUnsafeConfig(t *testing.T) {
	priv := mintKey(t)
	pub := &priv.PublicKey
	good := Config{
		Addr:       "127.0.0.1:0",
		Upstream:   "https://api.anthropic.com",
		MintKeys:   map[string]*rsa.PublicKey{mint.KeyID(pub): pub},
		Spent:      mint.NewSpentSet(),
		Credential: Credential{Header: "x-api-key", Value: "k"},
	}

	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"plaintext upstream", func(c *Config) { c.Upstream = "http://api.anthropic.com" }, "must be https"},
		{"no mint keys", func(c *Config) { c.MintKeys = nil }, "at least one mint key"},
		{"no spent set", func(c *Config) { c.Spent = nil }, "SpentSet is required"},
		{"no credential", func(c *Config) { c.Credential.Value = "" }, "Credential.Value is required"},
		{"misfiled key", func(c *Config) {
			c.MintKeys = map[string]*rsa.PublicKey{"mint-wrong": pub}
		}, "is actually"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.mut(&cfg)
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}

	if _, err := New(good); err != nil {
		t.Fatalf("New() on a good config: %v", err)
	}
}

func TestTokenWireFormatRoundTrips(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.token(t)

	back, err := mint.ParseToken(tok.Encode())
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if err := mint.Verify(h.m.PublicKey(), back); err != nil {
		t.Fatalf("a token did not survive the wire format: %v", err)
	}

	for _, bad := range []string{"", "onefield", "two.fields", "a.b.c.d", strings.Repeat("x", mint.MaxTokenBytes+1)} {
		if _, err := mint.ParseToken(bad); err == nil {
			t.Fatalf("ParseToken(%.20q) succeeded, want an error", bad)
		}
	}
}
