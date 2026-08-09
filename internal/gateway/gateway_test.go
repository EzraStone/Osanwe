package gateway

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

const pooledKey = "sk-pooled-the-client-must-never-see-this"

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

type failingRedemptionStore struct{ err error }

func (s failingRedemptionStore) Spend(*mint.Token) error  { return s.err }
func (s failingRedemptionStore) Refund(*mint.Token) error { return s.err }

type failingBudget struct{ err error }

func (b failingBudget) Reserve(context.Context, BudgetRequest) (BudgetReservation, error) {
	return nil, b.err
}

type recordingBudget struct {
	mu      sync.Mutex
	request BudgetRequest
}

func (b *recordingBudget) Reserve(_ context.Context, request BudgetRequest) (BudgetReservation, error) {
	b.mu.Lock()
	b.request = request
	b.mu.Unlock()
	return unlimitedReservation{}, nil
}

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
	gw           *Server
	m            *mint.Mint
	spent        *mint.SpentSet
	provider     *seen
	providerHTTP *httptest.Server
	client       *http.Client
	url          string
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
		Models:          []string{"demo"},
		MintKeys:        map[string]*rsa.PublicKey{m.KeyID(): m.PublicKey()},
		Spent:           spent,
		Budget:          UnlimitedBudget{},
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

	return &harness{
		gw: gw, m: m, spent: spent, provider: obs, providerHTTP: provider,
		client: front.Client(), url: front.URL,
	}
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
	req, err := http.NewRequest(http.MethodPost, h.url+"/v1/messages",
		strings.NewReader(`{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
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

func TestGatewayReservesTheConfiguredProviderCostBeforeDispatch(t *testing.T) {
	h := newHarness(t, nil)
	budget := &recordingBudget{}
	rates := CostRates{InputMicrosPerMillion: 3_000_000, OutputMicrosPerMillion: 15_000_000}
	h.gw.cfg.Budget = budget
	h.gw.cfg.Cost = rates

	resp := h.post(t, h.token(t), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	budget.mu.Lock()
	request := budget.request
	budget.mu.Unlock()
	want, err := EstimateCostMicros(request.InputBytes, request.MaxOutputTokens, rates)
	if err != nil {
		t.Fatal(err)
	}
	if request.Model != "demo" || request.CostMicros != want || want == 0 {
		t.Fatalf("reservation = %+v, want cost %d", request, want)
	}
}

func TestAggregateBudgetRefusalDoesNotSpendOrForwardTheToken(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.token(t)
	h.gw.cfg.Budget = failingBudget{err: &BudgetLimitError{Reset: time.Now().Add(time.Minute)}}

	resp := h.post(t, tok, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeRejected)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("budget refusal omitted Retry-After")
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatalf("provider received %d requests, want none", hits)
	}
	if err := h.spent.Spend(tok); err != nil {
		t.Fatalf("token was consumed by a budget refusal: %v", err)
	}
}

func TestAggregateBudgetFailureFailsClosedWithoutTakingPayment(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.token(t)
	h.gw.cfg.Budget = failingBudget{err: errors.New("budget disk is unavailable")}

	resp := h.post(t, tok, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeRejected)
	}
	if err := h.spent.Spend(tok); err != nil {
		t.Fatalf("token was consumed by a budget-store failure: %v", err)
	}
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

// The pooled credential is the gateway's, and only its fixed provider request
// should be decorated with it.
func TestPooledCredentialIsUsedForTheProviderRequest(t *testing.T) {
	h := newHarness(t, nil)
	resp := h.post(t, h.token(t), nil)
	defer resp.Body.Close()

	headers, _, _ := h.provider.snapshot()
	if got := headers.Get("x-api-key"); got != pooledKey {
		t.Fatalf("provider saw x-api-key %q, want the pooled key", got)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
}

func TestProviderCredentialEchoHeadersAreRemoved(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Api-Key", pooledKey)
		w.Header().Set("Authorization", "Bearer "+pooledKey)
		w.Header().Set("X-Debug-Credential", pooledKey)
		w.Header().Set("X-Safe-Provider-Header", "safe")
		fmt.Fprint(w, `{"ok":true}`)
	})
	resp := h.post(t, h.token(t), nil)
	defer resp.Body.Close()
	for _, name := range []string{"X-Api-Key", "Authorization", "X-Debug-Credential"} {
		if got := resp.Header.Get(name); got != "" {
			t.Fatalf("provider credential echo survived in %s: %q", name, got)
		}
	}
	if got := resp.Header.Get("X-Safe-Provider-Header"); got != "safe" {
		t.Fatalf("unrelated provider header = %q, want safe", got)
	}
}

// A client must not be able to substitute their own account, nor override the
// gateway's credential with one of their choosing.
func TestClientCredentialsAreStrippedNotForwarded(t *testing.T) {
	h := newHarness(t, nil)

	extra := http.Header{}
	extra.Set("Authorization", "Bearer sk-the-clients-own-account")
	extra.Set("X-Api-Key", "sk-attempted-override")
	extra.Set("Api-Key", "sk-another-client-credential")
	extra.Set("Cookie", "session=identifying")
	extra.Set("X-Forwarded-For", "203.0.113.7")
	extra.Set("User-Agent", "caller-sdk/9.8.7")
	extra.Set("Traceparent", "00-caller-trace-id-caller-span-id-01")
	extra.Set("X-Request-Id", "stable-caller-request-id")
	extra.Set("X-Stainless-Lang", "python")
	extra.Set("Anthropic-Version", "caller-specific-version")
	extra.Set("Accept", "application/json; caller-id=alice")
	extra.Set("Content-Type", "application/json; caller-id=alice")

	resp := h.post(t, h.token(t), extra)
	defer resp.Body.Close()

	headers, _, _ := h.provider.snapshot()
	if got := headers.Get("x-api-key"); got != pooledKey {
		t.Fatalf("x-api-key = %q; a client overrode the gateway's credential", got)
	}
	for _, h := range []string{
		"Authorization", "Api-Key", "Cookie", "X-Forwarded-For", "X-Real-Ip", "User-Agent",
		"Traceparent", "X-Request-Id", "X-Stainless-Lang",
	} {
		if got := headers.Get(h); got != "" {
			t.Fatalf("the provider received %s: %q, which identifies the caller", h, got)
		}
	}
	for name, want := range map[string]string{
		"Accept": canonicalAccept, "Content-Type": canonicalContentType, "Anthropic-Version": canonicalAnthropicVersion,
	} {
		if got := headers.Get(name); got != want {
			t.Fatalf("allowed API header %s = %q, want %q", name, got, want)
		}
	}
}

// A blind-signed token authorizes one bounded inference call. It must never
// turn the pooled account into a general provider API credential.
func TestOnlyMessagesInferenceCanReceiveThePooledCredential(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/messages"},
		{http.MethodDelete, "/v1/models/demo"},
		{http.MethodPost, "/v1/files"},
		{http.MethodPost, "/v1/batches"},
		{http.MethodPost, "/v1/fine_tuning/jobs"},
		{http.MethodPost, "/v1/messages?dangerous=true"},
		{http.MethodPost, "/v1/messages?"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, h.url+tc.path,
				strings.NewReader(`{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(TokenHeader, h.token(t).Encode())
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode < 400 {
				t.Fatalf("status = %d, want a policy refusal", resp.StatusCode)
			}
			if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
				t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeRejected)
			}
		})
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatalf("provider received %d non-inference requests", hits)
	}
	if h.spent.Len() != 0 {
		t.Fatalf("policy refusals reserved %d tokens", h.spent.Len())
	}
}

func TestSingleUpstreamEnforcesModelAndOutputCostLimits(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		name, body, namedField string
	}{
		{"model not allowed", `{"model":"most-expensive-unapproved-model","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, ""},
		{"output cap", `{"model":"demo","max_tokens":4097,"messages":[{"role":"user","content":"hello"}]}`, ""},
		{"missing output cap", `{"model":"demo","messages":[{"role":"user","content":"hello"}]}`, ""},
		{"zero output cap", `{"model":"demo","max_tokens":0,"messages":[{"role":"user","content":"hello"}]}`, ""},
		{"body cap", `{"model":"demo","max_tokens":1,"pad":"` + strings.Repeat("x", DefaultMaxRequestBody) + `"}`, ""},
		{"tools unsupported", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"tools":[]}`, "tools"},
		{"service tier unsupported", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"service_tier":"priority"}`, "service_tier"},
		{"messages empty", `{"model":"demo","max_tokens":64,"messages":[]}`, "messages"},
		{"messages wrong type", `{"model":"demo","max_tokens":64,"messages":{}}`, "messages"},
		{"message role invalid", `{"model":"demo","max_tokens":64,"messages":[{"role":"system","content":"x"}]}`, "messages"},
		{"message content invalid", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":null}]}`, "messages"},
		{"remote image content refused", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/huge.jpg"}}]}]}`, "messages"},
		{"message cache control refused", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello","cache_control":{"type":"ephemeral"}}]}`, "messages"},
		{"stream wrong type", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":"yes"}`, "stream"},
		{"temperature out of range", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"temperature":2}`, "temperature"},
		{"top p wrong type", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"top_p":"all"}`, "top_p"},
		{"stop sequences wrong type", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stop_sequences":"stop"}`, "stop_sequences"},
		{"stop sequence null element", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stop_sequences":["stop",null]}`, "stop_sequences"},
		{"system wrong type", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"system":42}`, "system"},
		{"system blocks refused", `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"system":[{"type":"text","text":"hello"}]}`, "system"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/messages", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(TokenHeader, h.token(t).Encode())
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			responseBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 400 {
				t.Fatalf("body %s returned %d, want a policy refusal", tc.body, resp.StatusCode)
			}
			if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
				t.Fatalf("token outcome = %q, want rejected", got)
			}
			if tc.namedField != "" && !strings.Contains(string(responseBody), tc.namedField) {
				t.Fatalf("refusal %s does not name unsupported field %q", responseBody, tc.namedField)
			}
		})
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatalf("provider received %d requests outside the configured cost policy", hits)
	}
	if h.spent.Len() != 0 {
		t.Fatalf("cost policy refusals reserved %d tokens", h.spent.Len())
	}
}

func TestProviderCannotForgeARefundOutcome(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(TokenOutcomeHeader, TokenOutcomeRefunded)
		fmt.Fprint(w, `{"ok":true}`)
	})
	extra := http.Header{TokenOutcomeHeader: []string{"client-made-this-up"}}
	resp := h.post(t, h.token(t), extra)
	defer resp.Body.Close()
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeSpent {
		t.Fatalf("provider forged outcome %q through gateway; want %q", got, TokenOutcomeSpent)
	}
	headers, _, _ := h.provider.snapshot()
	if got := headers.Get(TokenOutcomeHeader); got != "" {
		t.Fatalf("client outcome header reached provider as %q", got)
	}
}

func TestChunkedTrailersAreRejectedBeforeSpendingOrForwarding(t *testing.T) {
	h := newHarness(t, nil)
	req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/messages",
		strings.NewReader(`{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(TokenHeader, h.token(t).Encode())
	req.Trailer = http.Header{
		"Authorization": []string{"Bearer trailer-credential"},
		"X-Api-Key":     []string{"trailer-api-key"},
		"X-Stable-Id":   []string{"caller-alice"},
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", got)
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatalf("provider received a trailer-bearing request %d times", hits)
	}
	if h.spent.Len() != 0 {
		t.Fatalf("trailer-bearing request reserved %d tokens", h.spent.Len())
	}
}

func TestDuplicateJSONNamesAreRejectedBeforeSpendingAtEveryDepth(t *testing.T) {
	h := newHarness(t, nil)
	cases := map[string]string{
		"top-level model":  `{"model":"unapproved-expensive","model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
		"nested role":      `{"model":"demo","max_tokens":64,"messages":[{"role":"assistant","role":"user","content":"hello"}]}`,
		"nested content":   `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"first","content":"second"}]}`,
		"unsupported tree": `{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"metadata":{"id":1,"id":2}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/messages", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(TokenHeader, h.token(t).Encode())
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			responseBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
				t.Fatalf("outcome = %q, want rejected", got)
			}
			if !strings.Contains(string(responseBody), "duplicate") {
				t.Fatalf("response %q does not explain the duplicate", responseBody)
			}
		})
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatalf("provider saw %d ambiguous requests", hits)
	}
	if h.spent.Len() != 0 {
		t.Fatalf("duplicate requests reserved %d tokens", h.spent.Len())
	}
}

func TestRewriteClearsAlternateCallerURLRepresentations(t *testing.T) {
	h := newHarness(t, nil)
	inURL := &url.URL{
		Scheme: "http", Opaque: "//attacker.example/v1/files", User: url.UserPassword("u", "p"),
		Host: "attacker.example", Path: "/v1/messages", RawPath: "/v1/%66iles", OmitHost: true,
		ForceQuery: true, RawQuery: "admin=true", Fragment: "fragment", RawFragment: "raw-fragment",
	}
	in := &http.Request{Method: http.MethodPost, URL: inURL, Header: http.Header{TokenHeader: []string{"token"}}}
	out := in.Clone(context.Background())
	out.URL = new(url.URL)
	*out.URL = *inURL
	out.Header = in.Header.Clone()
	out.Trailer = http.Header{"Authorization": []string{"Bearer trailer"}}
	out.TransferEncoding = []string{"chunked"}
	h.gw.rewrite(&httputil.ProxyRequest{In: in, Out: out})

	if out.URL.Scheme != h.gw.upstream.Scheme || out.URL.Host != h.gw.upstream.Host || out.URL.Path != "/v1/messages" {
		t.Fatalf("rewritten URL = %#v", out.URL)
	}
	if out.URL.Opaque != "" || out.URL.User != nil || out.URL.RawPath != "" || out.URL.OmitHost || out.URL.ForceQuery ||
		out.URL.RawQuery != "" || out.URL.Fragment != "" || out.URL.RawFragment != "" {
		t.Fatalf("caller URL residue survived rewrite: %#v", out.URL)
	}
	if got := out.Header.Get(TokenHeader); got != "" {
		t.Fatalf("token survived rewrite: %q", got)
	}
	if got := out.Header.Get("x-api-key"); got != pooledKey {
		t.Fatalf("pooled credential = %q", got)
	}
	if len(out.Trailer) != 0 || len(out.TransferEncoding) != 0 {
		t.Fatalf("trailer state survived rewrite: trailers=%v transfer_encoding=%v", out.Trailer, out.TransferEncoding)
	}
}

func TestSingleUpstreamCatalogIsFreeAndUsesTheAllowlist(t *testing.T) {
	h := newHarness(t, nil)
	resp, err := h.client.Get(h.url + "/v1/models")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"id":"demo"`) {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatal("local model catalog was forwarded to the provider")
	}
	if h.spent.Len() != 0 {
		t.Fatal("free model catalog spent a token")
	}
}

func TestCatalogQueryIsRejectedLocallyWithoutSpending(t *testing.T) {
	h := newHarness(t, nil)
	for _, target := range []string{"http://gateway.test/v1/models?caller=alice", "http://gateway.test/v1/models?"} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set(TokenHeader, h.token(t).Encode())
			rec := httptest.NewRecorder()
			h.gw.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if got := rec.Header().Get(TokenOutcomeHeader); got != TokenOutcomeRejected {
				t.Fatalf("outcome = %q, want rejected", got)
			}
		})
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatalf("catalog query reached provider %d times", hits)
	}
	if h.spent.Len() != 0 {
		t.Fatalf("catalog query reserved %d tokens", h.spent.Len())
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

func TestRedemptionStoreFailureFailsClosedBeforeForwarding(t *testing.T) {
	h := newHarness(t, nil)
	h.gw.cfg.Spent = failingRedemptionStore{err: errors.New("disk is full")}
	resp := h.post(t, h.token(t), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeInvalid {
		t.Fatalf("token outcome = %q, want fail-closed %q", got, TokenOutcomeInvalid)
	}
	if _, _, hits := h.provider.snapshot(); hits != 0 {
		t.Fatal("request reached provider after redemption store failed")
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
			req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/messages",
				strings.NewReader(`{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set(TokenHeader, tok.Encode())
			req.Header.Set("Content-Type", "application/json")
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
// outcomes after dispatch
// --------------------------------------------------------------------------

// A 5xx is not proof that the provider did no work. It may have processed and
// billed the request before failing, so refunding here would enable free
// retries at the gateway operator's expense.
func TestProviderFailureStillSpendsTheToken(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream is having a bad day", http.StatusBadGateway)
	})
	tok := h.token(t)

	resp := h.post(t, tok, nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if h.spent.Len() != 1 {
		t.Fatal("the token was refunded even though the provider may already have processed the request")
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeSpent {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeSpent)
	}

	// It cannot be used again: the uncertain dispatch fails closed.
	h.provider.mu.Lock()
	h.provider.hits = 0
	h.provider.mu.Unlock()

	retry := h.post(t, tok, nil)
	defer retry.Body.Close()
	if retry.StatusCode != http.StatusConflict {
		t.Fatalf("retry status = %d, want 409 for a spent token", retry.StatusCode)
	}
}

// A connection that was never established is the one failure that proves the
// provider did no work, so it is the one failure that may be refunded. Making
// the user pay for the operator's outage would be charging for nothing.
func TestAConnectionThatWasNeverMadeRefundsTheToken(t *testing.T) {
	h := newHarness(t, nil)
	// Closing before the first request means the dial is refused outright:
	// there was no connection for a request to travel on.
	h.providerHTTP.Close()
	tok := h.token(t)

	resp := h.post(t, tok, nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if h.spent.Len() != 0 {
		t.Fatalf("spent set holds %d, want 0: the provider was never reached", h.spent.Len())
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeRefunded {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeRefunded)
	}
	if got := h.gw.metrics.Refunded.Load(); got != 1 {
		t.Fatalf("Refunded metric = %d, want 1", got)
	}

	// And the refund has to be real: the same token must buy a request again.
	if err := h.spent.Spend(tok); err != nil {
		t.Fatalf("the refunded token is still unusable: %v", err)
	}
}

// A failure after the request is on the wire is not proof of anything. The
// provider may have received, processed and billed it before the connection
// died, so refunding here would let a client harvest free inference by cutting
// the connection at the right moment.
func TestAFailureAfterDispatchStillSpendsTheToken(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		// Read the whole request, then hang up without answering. From the
		// gateway's side this is indistinguishable from a provider that did
		// the work and lost the connection on the way back.
		io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		conn.Close()
	})
	tok := h.token(t)

	resp := h.post(t, tok, nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if h.spent.Len() != 1 {
		t.Fatalf("spent set holds %d, want 1 after uncertain dispatch", h.spent.Len())
	}
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeSpent {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeSpent)
	}
	if got := h.gw.metrics.Refunded.Load(); got != 0 {
		t.Fatalf("Refunded metric = %d, want 0: this failure proves nothing", got)
	}
	retry := h.post(t, tok, nil)
	retry.Body.Close()
	if retry.StatusCode != http.StatusConflict {
		t.Fatalf("retry status = %d, want 409", retry.StatusCode)
	}
}

// A refund that did not survive is not a refund. If the store cannot record
// it, the client must be told the token is gone rather than put it back and
// have the gateway refuse it later.
func TestARefundThatCannotBeRecordedIsReportedAsSpent(t *testing.T) {
	h := newHarness(t, nil)
	h.providerHTTP.Close()
	tok := h.token(t)

	// Spend succeeds, refund fails: the store accepted the reservation and
	// then lost its ability to write.
	h.gw.cfg.Spent = halfBrokenStore{SpentSet: h.spent}

	resp := h.post(t, tok, nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeSpent {
		t.Fatalf("token outcome = %q, want %q when the refund could not be recorded", got, TokenOutcomeSpent)
	}
	if got := h.gw.metrics.Refunded.Load(); got != 0 {
		t.Fatalf("Refunded metric = %d, want 0: nothing was refunded", got)
	}
}

type halfBrokenStore struct{ *mint.SpentSet }

func (s halfBrokenStore) Refund(*mint.Token) error {
	return errors.New("the redemption journal is unwritable")
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
	if got := resp.Header.Get(TokenOutcomeHeader); got != TokenOutcomeSpent {
		t.Fatalf("token outcome = %q, want %q", got, TokenOutcomeSpent)
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
		Models:     []string{"demo"},
		MintKeys:   map[string]*rsa.PublicKey{mint.KeyID(pub): pub},
		Spent:      mint.NewSpentSet(),
		Budget:     UnlimitedBudget{},
		Credential: Credential{Header: "x-api-key", Value: "k"},
	}

	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"plaintext upstream", func(c *Config) { c.Upstream = "http://api.anthropic.com" }, "must be https"},
		{"no mint keys", func(c *Config) { c.MintKeys = nil }, "at least one mint key"},
		{"no redemption store", func(c *Config) { c.Spent = nil }, "RedemptionStore is required"},
		{"no aggregate budget", func(c *Config) { c.Budget = nil }, "Budget is required"},
		{"no credential", func(c *Config) { c.Credential.Value = "" }, "Credential.Value is required"},
		{"unsupported credential header", func(c *Config) { c.Credential.Header = "Cookie" }, "Credential.Header must be"},
		{"credential value newline", func(c *Config) { c.Credential.Value = "k\r\nX-Leak: yes" }, "control characters"},
		{"credential prefix newline", func(c *Config) { c.Credential.Prefix = "Bearer\n" }, "control characters"},
		{"no model allowlist", func(c *Config) { c.Models = nil }, "Models is required"},
		{"cost ceiling without prices", func(c *Config) { c.RequireCostRates = true }, "requires both input and output prices"},
		{"partial prices", func(c *Config) { c.Cost.InputMicrosPerMillion = 1 }, "both input and output prices"},
		{"request limit too high", func(c *Config) { c.MaxRequestBody = MaximumMaxRequestBody + 1 }, "MaxRequestBody"},
		{"output limit too high", func(c *Config) { c.MaxOutputTokens = MaximumMaxOutputTokens + 1 }, "MaxOutputTokens"},
		{"upstream query", func(c *Config) { c.Upstream = "https://api.anthropic.com?key=value" }, "must not contain"},
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
