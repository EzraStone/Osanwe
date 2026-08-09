package gateway

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/EzraStone/osanwe/internal/mint"
)

// routedHarness is a gateway fronting two providers, so a test can check which
// one a request actually reached.
type routedHarness struct {
	gw     *Server
	m      *mint.Mint
	spent  *mint.SpentSet
	client *http.Client
	url    string

	mu   sync.Mutex
	hits map[string][]http.Header
}

func (h *routedHarness) seen(name string) []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]http.Header(nil), h.hits[name]...)
}

func newRoutedHarness(t *testing.T) *routedHarness {
	t.Helper()
	h := &routedHarness{hits: map[string][]http.Header{}}

	roots := x509.NewCertPool()

	// Each stand-in answers in its own API's shape, and refuses the other
	// API's path. A mock that accepted anything and replied in one shape is
	// what hid the missing translation the first time round.
	provider := func(name string, style Style) string {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			h.mu.Lock()
			h.hits[name] = append(h.hits[name], r.Header.Clone())
			h.mu.Unlock()

			want := "/v1/messages"
			if style == StyleOpenAI {
				want = "/v1/chat/completions"
			}
			if r.URL.Path != want {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprintf(w, `{"error":{"message":"Unknown request URL: %s"}}`, r.URL.Path)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			if style == StyleOpenAI {
				fmt.Fprintf(w, `{"id":"c1","model":"m","choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, name)
				return
			}
			fmt.Fprintf(w, `{"type":"message","content":[{"type":"text","text":%q}]}`, name)
		}))
		t.Cleanup(srv.Close)
		roots.AddCert(srv.Certificate())
		return srv.URL
	}
	anthropicish := provider("anthropic", StyleAnthropic)
	openaiish := provider("openai", StyleOpenAI)

	routes, err := NewRoutes([]Route{
		{Model: "claude-sonnet-5", Style: StyleAnthropic, Upstream: anthropicish, Credential: "sk-ant-pooled", CredentialEnv: "ANTHROPIC_API_KEY"},
		{Model: "deepseek-chat", Style: StyleOpenAI, Upstream: openaiish, Credential: "sk-ds-pooled", CredentialEnv: "DEEPSEEK_API_KEY"},
	})
	if err != nil {
		t.Fatalf("NewRoutes: %v", err)
	}

	m, err := mint.New(mintKey(t), mint.OpenAuthorizer{})
	if err != nil {
		t.Fatalf("mint.New: %v", err)
	}
	h.m = m
	h.spent = mint.NewSpentSet()

	gw, err := New(Config{
		Addr:            "127.0.0.1:0",
		Routes:          routes,
		MintKeys:        map[string]*rsa.PublicKey{m.KeyID(): m.PublicKey()},
		Spent:           h.spent,
		Budget:          UnlimitedBudget{},
		UpstreamRootCAs: roots,
		Logger:          quiet(),
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	h.gw = gw

	front := httptest.NewServer(gw.Handler())
	t.Cleanup(front.Close)
	h.client, h.url = front.Client(), front.URL
	return h
}

func (h *routedHarness) token(t *testing.T) *mint.Token {
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

func (h *routedHarness) ask(t *testing.T, model string) *http.Response {
	t.Helper()
	body := `{"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	if model != "" {
		body = fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
	}
	req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(TokenHeader, h.token(t).Encode())
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// --------------------------------------------------------------------------

// One endpoint, several providers, chosen by the name in the request. This is
// what makes a single credential worth having.
func TestTheModelChoosesTheProvider(t *testing.T) {
	h := newRoutedHarness(t)

	resp := h.ask(t, "claude-sonnet-5")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "anthropic") {
		t.Fatalf("claude-sonnet-5 was answered by %s", body)
	}

	resp = h.ask(t, "deepseek-chat")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "openai") {
		t.Fatalf("deepseek-chat was answered by %s", body)
	}
}

// Each provider gets its own credential, in the shape it expects. Sending
// Anthropic's key to DeepSeek would leak one account's key to another vendor.
func TestEachProviderGetsItsOwnCredential(t *testing.T) {
	h := newRoutedHarness(t)
	h.ask(t, "claude-sonnet-5").Body.Close()
	h.ask(t, "deepseek-chat").Body.Close()

	anth := h.seen("anthropic")
	if len(anth) != 1 {
		t.Fatalf("anthropic saw %d requests, want 1", len(anth))
	}
	if got := anth[0].Get("x-api-key"); got != "sk-ant-pooled" {
		t.Fatalf("anthropic saw x-api-key %q", got)
	}
	if got := anth[0].Get("authorization"); got != "" {
		t.Fatalf("anthropic also saw an authorization header: %q", got)
	}
	if got := anth[0].Get("Anthropic-Version"); got != canonicalAnthropicVersion {
		t.Fatalf("anthropic API version = %q, want gateway-owned %q", got, canonicalAnthropicVersion)
	}

	oa := h.seen("openai")
	if len(oa) != 1 {
		t.Fatalf("openai saw %d requests, want 1", len(oa))
	}
	if got := oa[0].Get("authorization"); got != "Bearer sk-ds-pooled" {
		t.Fatalf("openai saw authorization %q", got)
	}
	if got := oa[0].Get("x-api-key"); got != "" {
		t.Fatalf("openai was sent an x-api-key: %q; that is another vendor's credential", got)
	}
	if got := oa[0].Get("Anthropic-Version"); got != "" {
		t.Fatalf("OpenAI-style provider received Anthropic-Version %q", got)
	}
}

// A model this gateway does not carry must be refused, and the token returned.
// Forwarding it would turn a typo into a charge: the client pays, reaches a
// provider that has never heard of the name, and gets an error anyway.
func TestAnUnknownModelIsRefusedAndTheTokenReturned(t *testing.T) {
	h := newRoutedHarness(t)

	resp := h.ask(t, "gpt-9-imaginary")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	// The message has to name what is available, or a typo becomes a support
	// conversation rather than a one-line fix.
	for _, want := range []string{"claude-sonnet-5", "deepseek-chat"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("refusal %s does not say that %s is available", body, want)
		}
	}
	if h.spent.Len() != 0 {
		t.Fatal("the token stayed spent on a request that went nowhere")
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeRejected)
	}
	if len(h.seen("anthropic"))+len(h.seen("openai")) != 0 {
		t.Fatal("an unroutable request still reached a provider")
	}
}

func TestARequestWithNoModelIsRefused(t *testing.T) {
	h := newRoutedHarness(t)
	resp := h.ask(t, "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "names no model") {
		t.Fatalf("refusal %s does not explain the problem", body)
	}
	if h.spent.Len() != 0 {
		t.Fatal("the token stayed spent")
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeRejected)
	}
}

// The catalog is free. Charging to find out what is on offer, or making a
// client spend a token to discover a typo, would be an odd way to run a shop.
func TestTheCatalogNeedsNoToken(t *testing.T) {
	h := newRoutedHarness(t)

	resp, err := h.client.Get(h.url + "/v1/models")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("catalog is not JSON: %v", err)
	}
	if len(out.Data) != 2 {
		t.Fatalf("catalog lists %d models, want 2", len(out.Data))
	}

	// Model names give the vendor away by themselves -- "claude-sonnet-5" is
	// not a secret -- so the property worth asserting is narrower and real:
	// no address a client could reach directly, and no credential.
	raw, _ := json.Marshal(out)
	for _, leak := range []string{"127.0.0.1", "https://", "http://", "sk-", "api_key", "credential"} {
		if strings.Contains(strings.ToLower(string(raw)), leak) {
			t.Fatalf("the catalog leaks %q: %s", leak, raw)
		}
	}
	if h.spent.Len() != 0 {
		t.Fatal("listing models spent a token")
	}
}

// --------------------------------------------------------------------------
// the route table itself
// --------------------------------------------------------------------------

func TestParseRoutes(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "sk-ant", "DEEPSEEK_API_KEY": "sk-ds"}
	get := func(k string) string { return env[k] }

	rt, err := ParseRoutes(strings.NewReader(`
# model            style      upstream                    env
claude-sonnet-5    anthropic  https://api.anthropic.com   ANTHROPIC_API_KEY

deepseek-chat      openai     https://api.deepseek.com    DEEPSEEK_API_KEY
`), get)
	if err != nil {
		t.Fatalf("ParseRoutes: %v", err)
	}
	if got := rt.Models(); len(got) != 2 {
		t.Fatalf("Models() = %v, want 2", got)
	}
	r, ok := rt.Lookup("deepseek-chat")
	if !ok {
		t.Fatal("deepseek-chat is missing")
	}
	if c := r.credential(); c.Header != "authorization" || c.Prefix != "Bearer " || c.Value != "sk-ds" {
		t.Fatalf("credential = %+v", c)
	}
}

// A route file belongs in version control. A key in it is a key published, so
// credentials come from the environment and a missing one is a startup error
// rather than a runtime surprise.
func TestAMissingCredentialIsReportedAtStartup(t *testing.T) {
	_, err := ParseRoutes(strings.NewReader(
		"a anthropic https://api.anthropic.com KEY_A\nb openai https://api.openai.com KEY_B\n"),
		func(string) string { return "" })
	if err == nil {
		t.Fatal("accepted routes with no credentials")
	}
	// Both, not just the first: an operator setting up five providers should
	// learn about all five at once.
	for _, want := range []string{"KEY_A", "KEY_B"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v does not mention %s", err, want)
		}
	}
}

func TestBadRouteTablesAreRefused(t *testing.T) {
	get := func(string) string { return "k" }
	cases := []struct{ name, text, want string }{
		{"wrong field count", "claude anthropic https://x\n", "want 4"},
		{"unknown style", "claude carrier-pigeon https://x K\n", "unknown style"},
		{"plaintext upstream", "claude anthropic http://x K\n", "must use https"},
		{"upstream query", "claude anthropic https://x?admin=true K\n", "must not contain"},
		{"duplicate model", "c anthropic https://x K\nc openai https://y K\n", "routed twice"},
		{"empty table", "# nothing but a comment\n", "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRoutes(strings.NewReader(tc.text), get)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestRouteCredentialControlCharactersAreRefused(t *testing.T) {
	_, err := ParseRoutes(strings.NewReader("claude anthropic https://api.anthropic.com KEY\n"),
		func(string) string { return "secret\r\nX-Leak: yes" })
	if err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("ParseRoutes error = %v, want a control-character refusal", err)
	}
}

// Routing means reading the body, so it must be bounded: without a cap a
// client could make the gateway hold anything it liked in memory.
func TestAnOversizedBodyIsRefused(t *testing.T) {
	h := newRoutedHarness(t)

	huge := strings.Repeat("x", DefaultMaxRequestBody+1024)
	req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-5","pad":"`+huge+`"}`))
	req.Header.Set(TokenHeader, h.token(t).Encode())
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if h.spent.Len() != 0 {
		t.Fatal("the token stayed spent on a request that was never forwarded")
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeRejected)
	}
}

// The body has to survive being read for routing. A gateway that routed
// correctly and then forwarded an empty body would be worse than one that did
// not route at all.
func TestTheBodySurvivesRouting(t *testing.T) {
	var got []byte
	var mu sync.Mutex

	roots := x509.NewCertPool()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = b
		mu.Unlock()
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()
	roots.AddCert(srv.Certificate())

	routes, err := NewRoutes([]Route{{Model: "m", Style: StyleAnthropic, Upstream: srv.URL, Credential: "k", CredentialEnv: "K"}})
	if err != nil {
		t.Fatalf("NewRoutes: %v", err)
	}
	m, _ := mint.New(mintKey(t), mint.OpenAuthorizer{})
	gw, err := New(Config{
		Addr: "127.0.0.1:0", Routes: routes,
		MintKeys: map[string]*rsa.PublicKey{m.KeyID(): m.PublicKey()},
		Spent:    mint.NewSpentSet(), Budget: UnlimitedBudget{}, UpstreamRootCAs: roots, Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	front := httptest.NewServer(gw.Handler())
	defer front.Close()

	bl, _ := mint.Blind(m.PublicKey())
	sig, _ := m.Issue(context.Background(), nil, bl.Blinded)
	tok, _ := bl.Unblind(sig)

	sent := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"a distinctive phrase"}],"system":"preserve-me","temperature":0.25}`
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", strings.NewReader(sent))
	req.Header.Set(TokenHeader, tok.Encode())
	req.Header.Set("Content-Type", "application/json")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	var gotJSON, sentJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("provider body is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(sent), &sentJSON); err != nil {
		t.Fatalf("test body is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotJSON, sentJSON) {
		t.Fatalf("provider received %#v, want semantic body %#v", gotJSON, sentJSON)
	}
}
