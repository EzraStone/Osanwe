package bearer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

// A dialer that connects and then immediately hangs up.
//
// These tests are about routing and the origin guard, not about talking to a
// provider. Closing at once means a proxied request fails fast through the
// error handler; a pipe left open instead blocks in the TLS handshake until
// the whole test binary is killed, which is how this was first written.
type nopDialer struct{}

func (nopDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

func testServer(t *testing.T, mut func(*Config)) *Server {
	t.Helper()
	cfg := Config{
		Addr:     "127.0.0.1:0",
		Upstream: "https://api.anthropic.com",
		Dialer:   nopDialer{},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mut != nil {
		mut(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// ask sends a request through the full handler, including the origin guard.
func ask(t *testing.T, s *Server, path string, headers map[string]string) *http.Response {
	return askMethod(t, s, http.MethodGet, path, headers)
}

func askMethod(t *testing.T, s *Server, method, path string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://127.0.0.1:8080"+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := &recorder{header: http.Header{}}
	s.http.Handler.ServeHTTP(rec, req)
	return rec.result()
}

type recorder struct {
	header http.Header
	code   int
	body   strings.Builder
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(c int)   { r.code = c }
func (r *recorder) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = 200
	}
	return r.body.Write(p)
}
func (r *recorder) result() *http.Response {
	if r.code == 0 {
		r.code = 200
	}
	return &http.Response{
		StatusCode: r.code,
		Header:     r.header,
		Body:       io.NopCloser(strings.NewReader(r.body.String())),
	}
}

// --------------------------------------------------------------------------
// the origin guard
// --------------------------------------------------------------------------

// Every page a user visits can reach 127.0.0.1. If bearer answered those
// requests, any website could spend the user's tokens and read which relay
// they are on, from a tab they forgot was open.
func TestRequestsFromOtherSitesAreRefused(t *testing.T) {
	s := testServer(t, nil)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"an origin that is not ours", map[string]string{"Origin": "https://evil.example"}},
		{"an http origin that is not ours", map[string]string{"Origin": "http://evil.example"}},
		{"the browser says cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"the browser says same-site", map[string]string{"Sec-Fetch-Site": "same-site"}},
		{"a name resolving here that is not ours", map[string]string{"Host": "evil.example:8080"}},
		{"an origin that merely looks loopback", map[string]string{"Origin": "https://127.0.0.1.evil.example"}},
		{"a subdomain of localhost owned by someone else", map[string]string{"Host": "notlocalhost:8080"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ask(t, s, Prefix+"status", tc.headers)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; a website could read this client's state", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "osanwe_cross_origin") {
				t.Fatalf("body = %s, want an explanation of the refusal", body)
			}
		})
	}

	if got := s.Metrics().CrossOrigin.Load(); got != int64(len(cases)) {
		t.Fatalf("CrossOrigin = %d, want %d", got, len(cases))
	}
}

// The guard must not break the clients that actually exist. The Anthropic SDK,
// curl and a Python script never send Origin, and requiring it would lock out
// every real user while buying nothing: a browser making a cross-origin
// request always sends one.
func TestOrdinaryClientsAreNotBlocked(t *testing.T) {
	s := testServer(t, nil)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"no origin at all, as an SDK sends", nil},
		{"our own origin", map[string]string{"Origin": "http://127.0.0.1:8080"}},
		{"localhost by name", map[string]string{"Origin": "http://localhost:8080"}},
		{"ipv6 loopback", map[string]string{"Origin": "http://[::1]:8080", "Host": "[::1]:8080"}},
		{"the browser says same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}},
		{"the browser says this was typed", map[string]string{"Sec-Fetch-Site": "none"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ask(t, s, Prefix+"status", tc.headers)
			if resp.StatusCode == http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("refused a legitimate client: %s", body)
			}
		})
	}
}

func TestAnExplicitlyAllowedOriginGetsThrough(t *testing.T) {
	s := testServer(t, func(c *Config) { c.AllowOrigins = []string{"https://console.example"} })

	if resp := ask(t, s, Prefix+"status", map[string]string{"Origin": "https://console.example"}); resp.StatusCode == http.StatusForbidden {
		t.Fatal("an explicitly allowed origin was refused")
	}
	if resp := ask(t, s, Prefix+"status", map[string]string{"Origin": "https://other.example"}); resp.StatusCode != http.StatusForbidden {
		t.Fatal("allowing one origin allowed a different one")
	}
}

// The guard covers the proxy too, not only bearer's own routes. A page that
// could not read status but could still send prompts would have gained the
// more valuable half.
func TestTheGuardCoversTheProxiedPathsToo(t *testing.T) {
	s := testServer(t, nil)
	resp := ask(t, s, "/v1/messages", map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a website could send prompts through this client", resp.StatusCode)
	}
}

// No CORS header may ever be granted. Adding one later "to make the interface
// work" would undo the entire guard, so its absence is asserted rather than
// assumed.
func TestNoCORSHeadersAreEverSent(t *testing.T) {
	s := testServer(t, nil)
	for _, headers := range []map[string]string{
		nil,
		{"Origin": "http://127.0.0.1:8080"},
		{"Origin": "https://evil.example"},
	} {
		resp := ask(t, s, Prefix+"status", headers)
		for name := range resp.Header {
			if strings.HasPrefix(strings.ToLower(name), "access-control-") {
				t.Fatalf("sent %s; granting cross-origin access is what this guard exists to prevent", name)
			}
		}
	}
}

// --------------------------------------------------------------------------
// status
// --------------------------------------------------------------------------

// stubRelays stands in for internal/pool.
type stubRelays struct {
	nick, addr string
	ok         bool
	n, signed  int
	since      time.Time
}

func (s stubRelays) Current() (string, string, bool) { return s.nick, s.addr, s.ok }
func (s stubRelays) Len() int                        { return s.n }
func (s stubRelays) SignedBy() int                   { return s.signed }
func (s stubRelays) GuardSince() (time.Time, bool)   { return s.since, !s.since.IsZero() }

func TestStatusReportsWhatTheInterfaceNeeds(t *testing.T) {
	s := testServer(t, func(c *Config) {
		c.Relays = stubRelays{
			nick: "ranger-thangorodrim", addr: "203.0.113.9:8443", ok: true,
			n: 5, signed: 3, since: time.Now().Add(-90 * time.Second),
		}
	})

	resp := ask(t, s, Prefix+"status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store; a stale relay name shown as current is a confident lie", got)
	}

	var st Status
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("status is not valid JSON: %v\n%s", err, body)
	}

	if st.Relay == nil || st.Relay.Nickname != "ranger-thangorodrim" {
		t.Fatalf("relay = %+v, want the one in use", st.Relay)
	}
	if !st.Relay.KeyMatched {
		t.Fatal("key_matched is false although a tunnel was established, which only happens when the pin matches")
	}
	if st.Relay.SinceSeconds < 60 {
		t.Fatalf("since_seconds = %d, want roughly 90", st.Relay.SinceSeconds)
	}
	if st.Directory == nil || st.Directory.SignedBy != 3 || st.Directory.RelaysKnown != 5 {
		t.Fatalf("directory = %+v, want 3 signatures over 5 relays", st.Directory)
	}
	if st.Paying != "your own key" {
		t.Fatalf("paying = %q, want the bring-your-own-key wording", st.Paying)
	}
	if st.Retained != "nothing" {
		t.Fatalf("retained = %q", st.Retained)
	}
}

// The status document is read by a page. Anything in it that could be spent or
// replayed would be a page that could spend it.
func TestStatusLeaksNoSecrets(t *testing.T) {
	const secret = "the-relay-shared-secret-value"
	s := testServer(t, func(c *Config) {
		c.ManualRelay = "203.0.113.9:8443"
		c.AllowOrigins = []string{"https://console.example"}
	})

	resp := ask(t, s, Prefix+"status", nil)
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	for _, forbidden := range []string{secret, "sha256/", "Bearer ", "sk-", "token\":\"", "nonce"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status contains %q, which a hostile page could read:\n%s", forbidden, text)
		}
	}
}

func TestStatusReportsTokenPaymentWhenItIsInUse(t *testing.T) {
	s := testServer(t, func(c *Config) { c.Tokens = stubWallet{on: 34, spent: 47} })

	resp := ask(t, s, Prefix+"status", nil)
	var st Status
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if st.Paying != "tokens" {
		t.Fatalf("paying = %q, want tokens", st.Paying)
	}
	if st.Wallet == nil || st.Wallet.OnHand != 34 || st.Wallet.Spent != 47 {
		t.Fatalf("wallet = %+v, want 34 on hand and 47 spent", st.Wallet)
	}
}

// Reserving a prefix only works if nothing else claims it. Provider APIs live
// under /v1, so a request there must still be proxied rather than swallowed.
func TestOnlyTheReservedPrefixIsHandledLocally(t *testing.T) {
	s := testServer(t, nil)

	resp := ask(t, s, Prefix+"status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the reserved path returned %d", resp.StatusCode)
	}

	// A proxied path reaches the transport, which here dials a pipe that never
	// answers, so anything but a local 200 means it was forwarded rather than
	// handled. What matters is that it was not treated as a status route.
	resp = ask(t, s, "/v1/models", nil)
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), `"retained"`) {
		t.Fatal("a provider path was answered with bearer's status document")
	}
}

// stubWallet stands in for internal/mint.Wallet.
type stubWallet struct {
	on    int
	spent uint64
}

func (s stubWallet) Take(context.Context) (*mint.Token, error) {
	return &mint.Token{KeyID: "mint-stub", Nonce: []byte("n"), Sig: []byte("s")}, nil
}
func (s stubWallet) Put(*mint.Token) {}
func (s stubWallet) Len() int        { return s.on }
func (s stubWallet) Spent() uint64   { return s.spent }

// A browser opening the interface asks for a favicon unprompted. Answer it
// locally with the browser-specific refusal; token mode's paid-path policy is
// an independent second layer that must also keep the wallet untouched.
func TestBrowserResourceRequestsAreNotForwarded(t *testing.T) {
	spender := &countingWallet{}
	s := testServer(t, func(c *Config) { c.Tokens = spender })

	for _, dest := range []string{"image", "document", "style", "script", "font"} {
		resp := ask(t, s, "/favicon.ico", map[string]string{"Sec-Fetch-Dest": dest})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Sec-Fetch-Dest %q returned %d, want 404", dest, resp.StatusCode)
		}
	}
	if spender.taken != 0 {
		t.Fatalf("bought %d tokens answering browser resource requests; opening the interface must not cost anything", spender.taken)
	}
}

// The rule must not catch the requests that matter. An API call from fetch
// reports "empty", and every non-browser client sends no such header at all.
func TestApiCallsStillReachTheProxy(t *testing.T) {
	spender := &countingWallet{}
	s := testServer(t, func(c *Config) { c.Tokens = spender })

	for _, headers := range []map[string]string{
		{"Sec-Fetch-Dest": "empty"},
		nil,
	} {
		resp := askMethod(t, s, http.MethodPost, "/v1/messages", headers)
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("an API call with headers %v was refused as a browser resource", headers)
		}
	}
	if spender.taken != 2 {
		t.Fatalf("took %d tokens for 2 API calls", spender.taken)
	}
}

type countingWallet struct {
	taken int
}

func (c *countingWallet) Take(context.Context) (*mint.Token, error) {
	c.taken++
	return &mint.Token{KeyID: "mint-stub", Nonce: []byte("n"), Sig: []byte("s")}, nil
}
func (c *countingWallet) Put(*mint.Token) {}

// Failures are read by a person. "dial tcp 127.0.0.1:8443: connect:
// connection refused" is precise, useless at the keyboard, and says nothing
// about what to do -- which is exactly what the interface showed the first
// time a relay was killed underneath it.
func TestFailuresAreExplainedInWords(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("tunnel: connecting to relay 127.0.0.1:8443: dial tcp: connect: connection refused"), "not answering"},
		{errors.New("tunnel: relay key mismatch\n  expected sha256/a\n  got sha256/b"), "different key"},
		{errors.New("relay 10.0.0.1:8443 rejected the credential (407)"), "OSANWE_SECRET"},
		{errors.New("relay 10.0.0.1:8443 will not carry traffic to x (403)"), "operator has to allow"},
		{errors.New("pool: no relay could carry a connection to x"), "No relay is available"},
		{errors.New("x509: certificate signed by unknown authority"), "could not be verified"},
		{context.DeadlineExceeded, "took too long"},
		{context.Canceled, "cancelled"},
	}
	for _, tc := range cases {
		got, recognised := explain(tc.err)
		if !recognised {
			t.Fatalf("explain(%v) did not recognise the failure", tc.err)
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("explain(%v) = %q, want something mentioning %q", tc.err, got, tc.want)
		}
		// No message may leak the machinery it is replacing.
		for _, jargon := range []string{"dial tcp", "x509:", "EOF", "0x"} {
			if strings.Contains(got, jargon) {
				t.Fatalf("explain(%v) = %q, which still contains %q", tc.err, got, jargon)
			}
		}
	}
}

// An unrecognised failure must not be dressed up as an explanation.
func TestAnUnknownFailureSaysSoRatherThanGuessing(t *testing.T) {
	got, recognised := explain(errors.New("something nobody anticipated"))
	if recognised {
		t.Fatal("claimed to recognise a failure it does not")
	}
	if strings.Contains(got, "something nobody anticipated") {
		t.Fatalf("message %q simply repeats the original", got)
	}
}

// The technical text still has to reach whoever is debugging a relay.
func TestTheOriginalErrorIsStillReturned(t *testing.T) {
	s := testServer(t, nil)
	resp := ask(t, s, "/v1/messages", nil)

	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Error struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("error body is not JSON: %v\n%s", err, body)
	}
	if out.Error.Message == "" {
		t.Fatal("no readable message")
	}
	if out.Error.Detail == "" {
		t.Fatal("the original error was dropped; an operator debugging a relay needs it")
	}
	if out.Error.Message == out.Error.Detail {
		t.Fatalf("message and detail are identical (%q); nothing was translated", out.Error.Message)
	}
}
