// Package gateway implements mithlond, the far side of the network.
//
// A relay hides who is asking. This hides what they are asking with: the
// client presents a blind-signed token instead of an account, and the gateway
// calls the provider using a credential the client never holds and never sees.
// The provider bills the gateway's account and learns nothing about which of
// its users produced any particular request.
//
// # What this component knows, and what that costs
//
// The gateway reads prompts. That is not an oversight, it is the deal: someone
// has to attach a real credential and speak to the provider, and TLS from the
// client cannot both terminate here and stay end-to-end. So the split is:
//
//	ranger    sees your address, never your words
//	mithlond  sees your words, never your address
//
// Neither sits on both sides, and the security of the whole arrangement is that
// they do not collude. This is the same assumption Apple's Private Relay makes,
// and it is stated in the package that depends on it rather than in a document
// nobody reads.
//
// # What is missing
//
// The design calls for this to run inside an attested TEE, so that "the
// gateway sees prompts" means the hardware sees them while the operator
// provably cannot, and a client can check the exact binary before sending
// anything. None of that is here. Until it is, running a gateway means asking
// users to trust its operator, and that should be said plainly to them rather
// than implied away by the word "anonymous".
package gateway

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

// TokenHeader carries the token. It is deliberately not Authorization, which
// belongs to the provider credential this gateway substitutes.
const TokenHeader = "X-Osanwe-Token"

// Timeouts. As in bearer, no overall response timeout is set: a model thinking
// for a long time and then streaming for longer is ordinary traffic here.
const (
	DefaultResponseHeaderTimeout = 5 * time.Minute
	DefaultReadHeaderTimeout     = 30 * time.Second
)

// Credential is how the gateway authenticates to the provider.
type Credential struct {
	// Header is the provider's credential header: "x-api-key" for Anthropic,
	// "authorization" for anything OpenAI-compatible.
	Header string

	// Prefix goes before the value, "Bearer " where the provider expects it.
	Prefix string

	// Value is the pooled key.
	Value string
}

func (c Credential) valid() error {
	if strings.TrimSpace(c.Header) == "" {
		return errors.New("gateway: Credential.Header is required")
	}
	if c.Value == "" {
		return errors.New("gateway: Credential.Value is required; a gateway with no provider credential cannot answer anything")
	}
	return nil
}

// Config configures a Server.
type Config struct {
	// Addr to listen on.
	Addr string

	// Upstream is the provider base URL.
	Upstream string

	// MintKeys are the mint public keys whose tokens this gateway accepts,
	// by key id. Several are held at once so a key rotation has an overlap
	// window instead of invalidating everything in flight.
	MintKeys map[string]*rsa.PublicKey

	// Spent records redeemed tokens. Required: without it every token is
	// infinitely reusable.
	Spent *mint.SpentSet

	// Credential authenticates the gateway to the provider.
	Credential Credential

	// UpstreamRootCAs overrides the roots used to verify the provider.
	//
	// It exists for a self-hosted or enterprise provider endpoint presenting a
	// privately-issued certificate, which would otherwise be unreachable. As
	// in bearer, there is deliberately no option to skip verification: the
	// pooled credential is attached to this connection, and a gateway that
	// could be talked into trusting anything would hand it to whoever asked.
	UpstreamRootCAs *x509.CertPool

	ResponseHeaderTimeout time.Duration
	Logger                *slog.Logger
}

// Metrics are cumulative counters. There is deliberately no per-request
// record: a gateway that kept one would hold precisely the log this design
// exists to prevent anyone from having.
type Metrics struct {
	Accepted    atomic.Int64
	NoToken     atomic.Int64
	BadToken    atomic.Int64
	Replayed    atomic.Int64
	Refunded    atomic.Int64
	UpstreamErr atomic.Int64
}

// Server is the gateway.
type Server struct {
	cfg      Config
	log      *slog.Logger
	upstream *url.URL
	metrics  Metrics

	http     *http.Server
	proxy    *httputil.ReverseProxy
	listener net.Listener
}

// tokenKey carries the spent token to the response hooks, so a request that
// produced nothing can be refunded.
type tokenKey struct{}

// New validates a Config and returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("gateway: Addr is required")
	}
	if cfg.Upstream == "" {
		return nil, errors.New("gateway: Upstream is required")
	}
	if len(cfg.MintKeys) == 0 {
		return nil, errors.New("gateway: at least one mint key is required; a gateway with none would accept no token, or worse, any")
	}
	if cfg.Spent == nil {
		return nil, errors.New("gateway: a SpentSet is required; without one every token can be spent forever")
	}
	if err := cfg.Credential.valid(); err != nil {
		return nil, err
	}
	if cfg.ResponseHeaderTimeout <= 0 {
		cfg.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	up, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, fmt.Errorf("gateway: parsing upstream %q: %w", cfg.Upstream, err)
	}
	if up.Scheme != "https" {
		return nil, fmt.Errorf("gateway: upstream must be https, got %q; the pooled credential would otherwise cross the network in the clear", cfg.Upstream)
	}
	if up.Host == "" {
		return nil, fmt.Errorf("gateway: upstream %q has no host", cfg.Upstream)
	}

	// Every key must be usable, checked now rather than at the first request.
	for id, k := range cfg.MintKeys {
		if k == nil {
			return nil, fmt.Errorf("gateway: mint key %q is nil", id)
		}
		if got := mint.KeyID(k); got != id {
			return nil, fmt.Errorf("gateway: mint key filed under %q is actually %s", id, got)
		}
	}

	s := &Server{cfg: cfg, log: cfg.Logger, upstream: up}
	s.proxy = &httputil.ReverseProxy{
		Rewrite:   s.rewrite,
		Transport: s.transport(),
		// -1 flushes each write immediately, which is what keeps server-sent
		// events arriving token by token instead of in one lump at the end.
		FlushInterval:  -1,
		ModifyResponse: s.modifyResponse,
		ErrorHandler:   s.handleError,
	}
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           http.HandlerFunc(s.ServeHTTP),
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
	}
	return s, nil
}

func (s *Server) transport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    s.cfg.UpstreamRootCAs,
		},
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: s.cfg.ResponseHeaderTimeout,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
	}
}

// ServeHTTP takes payment, then proxies.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.metrics.Accepted.Add(1)

	raw := r.Header.Get(TokenHeader)
	if raw == "" {
		s.metrics.NoToken.Add(1)
		s.refuse(w, http.StatusUnauthorized, "no_token",
			"This gateway takes a token, not an API key. Present one in "+TokenHeader+".")
		return
	}

	tok, err := mint.ParseToken(raw)
	if err != nil {
		s.metrics.BadToken.Add(1)
		// The parse error is not echoed. It describes bytes the client sent,
		// so it tells them nothing they do not know, and reflecting attacker
		// input into a response is a habit worth not having.
		s.refuse(w, http.StatusUnauthorized, "bad_token", "That token could not be read.")
		return
	}

	key, ok := s.cfg.MintKeys[tok.KeyID]
	if !ok {
		s.metrics.BadToken.Add(1)
		s.refuse(w, http.StatusUnauthorized, "unknown_mint",
			"That token was signed by a mint this gateway does not accept, or by a key that has been retired.")
		return
	}
	if err := mint.Verify(key, tok); err != nil {
		s.metrics.BadToken.Add(1)
		s.refuse(w, http.StatusUnauthorized, "bad_token", "That token is not valid.")
		return
	}

	// Spend before forwarding, never after. Marking it afterwards would leave
	// a window in which the same token, sent concurrently, buys several
	// requests -- and that window is exactly what an attacker would aim for.
	if err := s.cfg.Spent.Spend(tok); err != nil {
		s.metrics.Replayed.Add(1)
		s.refuse(w, http.StatusConflict, "token_spent", "That token has already been used.")
		return
	}

	r = r.WithContext(context.WithValue(r.Context(), tokenKey{}, tok))
	s.proxy.ServeHTTP(w, r)
}

// rewrite points the request at the provider and replaces the client's
// credentials with the gateway's own.
func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	pr.Out.URL.Scheme = s.upstream.Scheme
	pr.Out.URL.Host = s.upstream.Host
	pr.Out.Host = s.upstream.Host

	if base := strings.TrimSuffix(s.upstream.Path, "/"); base != "" {
		pr.Out.URL.Path = base + pr.Out.URL.Path
	}

	// The token stops here. Forwarding it would hand the provider a stable
	// identifier for one request and, worse, a bearer instrument.
	pr.Out.Header.Del(TokenHeader)

	// Anything the client sent that looks like a credential is dropped before
	// the pooled one is set. A client must not be able to override the
	// gateway's credential, and must not be able to smuggle their own account
	// through a service whose entire purpose is that they do not use one.
	pr.Out.Header.Del("Authorization")
	pr.Out.Header.Del("Proxy-Authorization")
	pr.Out.Header.Del("X-Api-Key")
	pr.Out.Header.Del("Api-Key")

	// Deliberately not calling SetXForwarded. The provider must not learn
	// where the request came from, and that is the whole point of this hop.
	pr.Out.Header.Del("X-Forwarded-For")
	pr.Out.Header.Del("X-Forwarded-Host")
	pr.Out.Header.Del("X-Forwarded-Proto")
	pr.Out.Header.Del("Forwarded")

	// Client hints and identifying metadata a tool may have attached.
	pr.Out.Header.Del("X-Real-Ip")
	pr.Out.Header.Del("Cookie")

	pr.Out.Header.Set(s.cfg.Credential.Header, s.cfg.Credential.Prefix+s.cfg.Credential.Value)
}

// modifyResponse refunds a token when the provider produced nothing usable.
//
// It runs after the response head arrives and before any body is copied, which
// is the last moment a refund is honest: past this point the client is getting
// output and has had what they paid for.
func (s *Server) modifyResponse(resp *http.Response) error {
	if resp.StatusCode < 500 {
		return nil
	}
	s.metrics.UpstreamErr.Add(1)
	if tok, ok := resp.Request.Context().Value(tokenKey{}).(*mint.Token); ok {
		s.cfg.Spent.Refund(tok)
		s.metrics.Refunded.Add(1)
		// Status only. Logging the token would create a record that a
		// particular token was used at a particular moment, which is half of
		// the link this design removes.
		s.log.Warn("provider failed; token refunded", "status", resp.StatusCode)
	}
	return nil
}

// handleError refunds and reports a transport failure.
func (s *Server) handleError(w http.ResponseWriter, r *http.Request, err error) {
	s.metrics.UpstreamErr.Add(1)
	if tok, ok := r.Context().Value(tokenKey{}).(*mint.Token); ok {
		s.cfg.Spent.Refund(tok)
		s.metrics.Refunded.Add(1)
	}
	// The path is logged; the body never is, and neither is the token.
	s.log.Error("upstream request failed", "path", r.URL.Path, "error", err)
	s.refuse(w, http.StatusBadGateway, "upstream_error",
		"The provider could not be reached. Your token has not been used.")
}

// refuse writes an error in the shape a model client expects, so an existing
// tool surfaces it rather than choking on it.
func (s *Server) refuse(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`+"\n", "osanwe_"+kind, message)
}

// Metrics returns the counters.
func (s *Server) Metrics() *Metrics { return &s.metrics }

// Handler exposes the gateway as an http.Handler, for tests and for embedding.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.ServeHTTP) }

// Addr returns the address actually bound.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Listen binds without serving, so a caller can learn the assigned port.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("gateway: listening on %s: %w", s.cfg.Addr, err)
	}
	s.listener = ln
	return nil
}

// Serve accepts requests until shutdown. It calls Listen if needed.
//
// TLS is the caller's to provide, because a gateway is reached through a relay
// and the relay must not be able to read what it carries. Serving this
// unencrypted on anything but loopback would hand every prompt to the hop in
// front of it.
func (s *Server) Serve(tlsConf *tls.Config) error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	ln := s.listener
	if tlsConf != nil {
		ln = tls.NewListener(ln, tlsConf)
	}
	err := s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the server, waiting for in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
