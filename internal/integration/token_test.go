package integration

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
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

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/bearer"
	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/gateway"
	"github.com/EzraStone/osanwe/internal/mint"
	"github.com/EzraStone/osanwe/internal/policy"
	"github.com/EzraStone/osanwe/internal/ranger"
	"github.com/EzraStone/osanwe/internal/tunnel"
)

const providerKey = "sk-pooled-provider-key-nobody-else-holds"

var (
	tokenKeyOnce sync.Once
	tokenTestKey *rsa.PrivateKey
)

func mintSigningKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	tokenKeyOnce.Do(func() {
		k, err := mint.GenerateKey(mint.MinKeyBits)
		if err != nil {
			panic(err)
		}
		tokenTestKey = k
	})
	return tokenTestKey
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// providerView records what the provider was actually sent.
type providerView struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []string
}

func (p *providerView) record(h http.Header, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.headers = append(p.headers, h.Clone())
	p.bodies = append(p.bodies, body)
}

func (p *providerView) snapshot() ([]http.Header, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]http.Header(nil), p.headers...), append([]string(nil), p.bodies...)
}

// gatewayView records what arrived at the gateway.
//
// This observation point is the only way to test what bearer sends. Checking
// the provider instead proves nothing about the client: the gateway strips
// credentials too, so a bearer that forwarded the user's key would still look
// clean from the far side. The claim being made is that the key never leaves
// the user's machine, and that has to be measured at the first hop it would
// reach.
type gatewayView struct {
	mu      sync.Mutex
	headers []http.Header
}

func (g *gatewayView) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.headers = append(g.headers, r.Header.Clone())
		g.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (g *gatewayView) snapshot() []http.Header {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]http.Header(nil), g.headers...)
}

// chain is the whole Phase 3 path: a tool talks to bearer, bearer buys tokens
// from the mint and tunnels through a relay to the gateway, and the gateway
// calls the provider with a pooled key.
type chain struct {
	toolEndpoint string
	provider     *providerView
	atGateway    *gatewayView
	mintSrv      *mint.Mint
	spent        *mint.SpentSet
	wallet       *mint.Wallet
	relayBytes   func() []byte
}

func newChain(t *testing.T) *chain {
	t.Helper()

	// --- provider ---------------------------------------------------------
	view := &providerView{}
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
	providerSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		view.record(r.Header, string(body))
		fmt.Fprint(w, `{"content":"the model answered"}`)
	})}
	go func() { _ = providerSrv.Serve(providerLn) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = providerSrv.Shutdown(ctx)
	})
	providerRoots := x509.NewCertPool()
	providerRoots.AddCert(providerCert.Leaf)

	// --- mint -------------------------------------------------------------
	m, err := mint.New(mintSigningKey(t), mint.OpenAuthorizer{})
	if err != nil {
		t.Fatalf("mint.New: %v", err)
	}
	mintHTTP := httptest.NewServer(mint.NewServer(m, discard()).Handler())
	t.Cleanup(mintHTTP.Close)

	// --- gateway ----------------------------------------------------------
	spent := mint.NewSpentSet()
	atGateway := &gatewayView{}
	gw, err := gateway.New(gateway.Config{
		Addr:     "127.0.0.1:0",
		Upstream: "https://" + providerLn.Addr().String(),
		MintKeys: map[string]*rsa.PublicKey{m.KeyID(): m.PublicKey()},
		Spent:    spent,
		Models:   []string{"test"},
		// The stand-in provider is self-signed, so the gateway needs its root.
		// There is no option anywhere to skip verification.
		UpstreamRootCAs: providerRoots,
		Credential:      gateway.Credential{Header: "x-api-key", Value: providerKey},
		Logger:          discard(),
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	// Served with a recorder in front, so the test can see exactly what bearer
	// put on the wire.
	gwHTTP := httptest.NewUnstartedServer(atGateway.wrap(gw.Handler()))
	gwHTTP.StartTLS()
	t.Cleanup(gwHTTP.Close)

	gwAddr := strings.TrimPrefix(gwHTTP.URL, "https://")
	gwRoots := x509.NewCertPool()
	gwRoots.AddCert(gwHTTP.Certificate())

	// --- tap, standing in for a packet capture on the relay host ----------
	tp := newTap(t, gwAddr)

	// --- relay ------------------------------------------------------------
	relayCert, relayPin, err := certs.SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("relay cert: %v", err)
	}
	allowlist, err := policy.Parse([]string{tp.Addr()})
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	authenticator, err := auth.New(secret)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	relay, err := ranger.New(ranger.Config{
		Addr:      "127.0.0.1:0",
		TLS:       &tls.Config{Certificates: []tls.Certificate{relayCert}},
		Allowlist: allowlist,
		Auth:      authenticator,
	})
	if err != nil {
		t.Fatalf("ranger.New: %v", err)
	}
	if err := relay.Listen(); err != nil {
		t.Fatalf("relay listen: %v", err)
	}
	go func() { _ = relay.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = relay.Shutdown(ctx)
	})

	// --- wallet and bearer ------------------------------------------------
	client := &mint.Client{URL: mintHTTP.URL, ExpectKeyID: m.KeyID()}
	wallet := mint.NewWallet(client, "", 4)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go wallet.Run(ctx)

	dialer, err := tunnel.New(tunnel.Config{Relay: relay.Addr().String(), Pin: relayPin, Secret: secret})
	if err != nil {
		t.Fatalf("tunnel.New: %v", err)
	}
	client2, err := bearer.New(bearer.Config{
		Addr:            "127.0.0.1:0",
		Upstream:        "https://" + tp.Addr(),
		Dialer:          dialer,
		Tokens:          wallet,
		UpstreamRootCAs: gwRoots,
		Logger:          discard(),
	})
	if err != nil {
		t.Fatalf("bearer.New: %v", err)
	}
	if err := client2.Listen(); err != nil {
		t.Fatalf("bearer listen: %v", err)
	}
	go func() { _ = client2.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client2.Shutdown(ctx)
	})

	return &chain{
		toolEndpoint: client2.Addr().String(),
		provider:     view,
		atGateway:    atGateway,
		mintSrv:      m,
		spent:        spent,
		wallet:       wallet,
		relayBytes:   tp.Bytes,
	}
}

func (c *chain) ask(t *testing.T, prompt string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+c.toolEndpoint+"/v1/messages",
		strings.NewReader(fmt.Sprintf(`{"model":"test","max_tokens":16,"messages":[{"role":"user","content":%q}]}`, prompt)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// A tool that still has the user's key configured will send it. It must
	// not leave the machine.
	req.Header.Set("X-Api-Key", "sk-the-users-personal-account-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// TestTheWholeChainBuysAndSpendsAToken walks the complete Phase 3 path and
// checks, at each boundary, that the thing which must not cross it has not.
func TestTheWholeChainBuysAndSpendsAToken(t *testing.T) {
	c := newChain(t)
	const prompt = "a distinctive phrase that should never appear at the relay"

	resp, body := c.ask(t, prompt)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "the model answered") {
		t.Fatalf("body = %q, want the provider's answer", body)
	}

	headers, bodies := c.provider.snapshot()
	if len(headers) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(headers))
	}

	// The provider was paid for by the gateway's pooled account.
	if got := headers[0].Get("x-api-key"); got != providerKey {
		t.Fatalf("provider saw x-api-key %q, want the gateway's pooled key", got)
	}

	// The user's own key never left their machine -- checked at the gateway,
	// which is the first place it could have arrived. Checking only the
	// provider would prove nothing, since the gateway strips credentials too.
	atGw := c.atGateway.snapshot()
	if len(atGw) != 1 {
		t.Fatalf("gateway saw %d requests, want 1", len(atGw))
	}
	for _, seen := range []http.Header{atGw[0], headers[0]} {
		for name, values := range seen {
			for _, v := range values {
				if strings.Contains(v, "sk-the-users-personal-account-key") {
					t.Fatalf("the user's own API key left the machine, in header %s", name)
				}
			}
		}
	}

	// And the gateway was in fact paid, so the check above is not passing
	// merely because nothing arrived.
	if got := atGw[0].Get(gateway.TokenHeader); got == "" {
		t.Fatal("no token reached the gateway")
	}

	// No token reached the provider.
	if got := headers[0].Get(gateway.TokenHeader); got != "" {
		t.Fatalf("the provider received a token: %q", got)
	}

	// The prompt did arrive, since the gateway is the component that reads it.
	if !strings.Contains(bodies[0], prompt) {
		t.Fatalf("the provider did not receive the prompt: %q", bodies[0])
	}

	// One token was spent.
	if c.spent.Len() != 1 {
		t.Fatalf("spent set holds %d, want 1", c.spent.Len())
	}
}

// The relay carries this traffic and must be able to read none of it: not the
// prompt, not the token, not the pooled key.
func TestRelayReadsNeitherPromptNorToken(t *testing.T) {
	c := newChain(t)
	const prompt = "another distinctive phrase for the packet capture"

	resp, body := c.ask(t, prompt)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	captured := c.relayBytes()

	// Controls, without which "no plaintext found" would mean nothing. A tap
	// that captured a handful of bytes, or nothing at all, would pass every
	// assertion below while proving the opposite of what is claimed. The floor
	// is well above the size of the request and response being searched for.
	const floor = 1024
	if len(captured) < floor {
		t.Fatalf("captured only %d bytes at the relay, under the %d floor; "+
			"an absence-of-plaintext test needs to have seen the traffic to mean anything", len(captured), floor)
	}
	if captured[0] != 0x16 {
		t.Fatalf("the first byte crossing the relay is %#x, not a TLS handshake record", captured[0])
	}

	for _, forbidden := range []struct{ name, value string }{
		{"the prompt", prompt},
		{"the pooled provider key", providerKey},
		{"the mint key id", c.mintSrv.KeyID()},
		{"the model's answer", "the model answered"},
	} {
		if strings.Contains(string(captured), forbidden.value) {
			t.Fatalf("%s is readable in what crossed the relay", forbidden.name)
		}
	}
}

// Each request buys and spends its own token, so a session is not one
// long-lived credential that ties its requests together.
func TestEachRequestSpendsItsOwnToken(t *testing.T) {
	c := newChain(t)

	const requests = 5
	for i := 0; i < requests; i++ {
		resp, body := c.ask(t, fmt.Sprintf("request %d", i))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d, body %s", i, resp.StatusCode, body)
		}
	}

	if got := c.spent.Len(); got != requests {
		t.Fatalf("spent %d tokens for %d requests", got, requests)
	}
	if got := c.mintSrv.Issued(); got < uint64(requests) {
		t.Fatalf("mint issued %d tokens for %d requests", got, requests)
	}

	headers, _ := c.provider.snapshot()
	if len(headers) != requests {
		t.Fatalf("provider saw %d requests, want %d", len(headers), requests)
	}
}

// A gateway that has never seen this mint must refuse the token, so a client
// cannot spend somewhere the mint has no arrangement.
func TestGatewayRefusesATokenFromAnUnknownMint(t *testing.T) {
	c := newChain(t)

	stranger, err := mint.GenerateKey(mint.MinKeyBits)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sm, err := mint.New(stranger, mint.OpenAuthorizer{})
	if err != nil {
		t.Fatalf("mint.New: %v", err)
	}
	bl, err := mint.Blind(sm.PublicKey())
	if err != nil {
		t.Fatalf("Blind: %v", err)
	}
	sig, err := sm.Issue(context.Background(), nil, bl.Blinded)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	tok, err := bl.Unblind(sig)
	if err != nil {
		t.Fatalf("Unblind: %v", err)
	}

	if err := mint.Verify(c.mintSrv.PublicKey(), tok); err == nil {
		t.Fatal("a token from another mint verified against this one")
	}
	if _, ok := map[string]bool{c.mintSrv.KeyID(): true}[tok.KeyID]; ok {
		t.Fatal("two independently generated mints produced the same key id")
	}
}

// A client that cannot buy must be told so, rather than having the request go
// through unpaid.
func TestRequestFailsWhenNoTokenCanBeBought(t *testing.T) {
	c := newChain(t)

	// Point the wallet at a mint that is not there.
	dead := &mint.Client{URL: "http://127.0.0.1:1", ExpectKeyID: c.mintSrv.KeyID()}
	if _, err := dead.Token(context.Background(), ""); err == nil {
		t.Fatal("bought a token from a mint that is not running")
	}
}
