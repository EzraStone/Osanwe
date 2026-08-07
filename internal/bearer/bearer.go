// Package bearer implements the client.
//
// It listens on loopback and forwards requests to a provider through a ranger,
// so an existing tool only has to change its base URL. Everything past the
// loopback listener travels inside a TLS session that terminates at the
// provider, which is what keeps the relay unable to read prompts.
//
// Bearer runs on the user's own machine. The plaintext hop from a tool to
// bearer never leaves the host, which is why binding to a non-loopback address
// is refused: that would put prompts on a network in the clear and quietly
// undo the property the rest of the system is built to provide.
package bearer

import (
	"context"
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

// DefaultUpstream is the provider bearer forwards to.
const DefaultUpstream = "https://api.anthropic.com"

// gatewayTokenHeader is where mithlond looks for a token. It is duplicated
// rather than imported so that the client does not depend on the gateway
// package; the two are deployed by different people and should not have to be
// built together.
const gatewayTokenHeader = "X-Osanwe-Token"

// Timeouts. ResponseHeaderTimeout is generous because a model may think for a
// long time before emitting its first token, and no overall response timeout
// is set at all: a long stream is normal traffic here, not a stuck request.
const (
	DefaultResponseHeaderTimeout = 5 * time.Minute
	DefaultReadHeaderTimeout     = 30 * time.Second
)

// Dialer opens a tunnel to a destination. internal/tunnel.Dialer satisfies it.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// TokenSource supplies one token per request. internal/mint.Wallet satisfies
// it.
//
// Setting one switches bearer from bring-your-own-key to paying with tokens:
// the upstream becomes a gateway rather than the provider, and the user's own
// credentials stop leaving this machine entirely.
type TokenSource interface {
	Take(ctx context.Context) (*mint.Token, error)

	// Put takes back a token that was never presented, so a request that
	// failed before reaching the gateway does not throw away something already
	// paid for.
	Put(*mint.Token)
}

// tokenKey carries the token from the handler to the rewrite hook.
type tokenKey struct{}

// Config configures a Server.
type Config struct {
	// Addr is the loopback address to listen on.
	Addr string

	// Upstream is the provider base URL.
	Upstream string

	// Dialer opens tunnels. Required.
	Dialer Dialer

	// Tokens, when set, pays for each request with a blind-signed token and
	// strips the user's own credentials before forwarding. Upstream must then
	// be a gateway, not a provider directly: a provider would reject a token
	// and has no idea what to do with one.
	Tokens TokenSource

	// Relays, when set, lets the client report which relay it is using.
	// internal/pool satisfies it.
	Relays RelayStatus

	// ManualRelay names a relay pinned by hand, purely so status can report it.
	// It is not used for dialling; the Dialer already holds that.
	ManualRelay string

	// AllowOrigins adds origins permitted to reach this client, beyond
	// loopback. Every entry is a page that may spend the user's tokens, so the
	// list is empty by default and adding to it is a deliberate act.
	AllowOrigins []string

	// UpstreamRootCAs overrides the roots used to verify the provider.
	//
	// For api.anthropic.com the system roots are correct and this stays nil.
	// It exists for a self-hosted or enterprise gateway presenting a
	// privately-issued certificate, which would otherwise be unreachable
	// without disabling verification -- and there is deliberately no option
	// to disable verification.
	UpstreamRootCAs *x509.CertPool

	// AllowNonLoopback permits binding a routable address. Doing so puts
	// prompts on the network in plaintext between the tool and bearer, so it
	// exists only for callers who have arranged their own protection and know
	// they have.
	AllowNonLoopback bool

	ResponseHeaderTimeout time.Duration
	Logger                *slog.Logger
}

// Metrics are cumulative counters. As with the relay, they hold no
// per-request detail: a client should not accumulate a log of its own prompts
// any more than a relay should.
type Metrics struct {
	Requests    atomic.Int64
	Upstream5xx atomic.Int64
	TunnelFails atomic.Int64
	NoToken     atomic.Int64
	CrossOrigin atomic.Int64
}

// Server is the local endpoint a tool points at.
type Server struct {
	cfg      Config
	log      *slog.Logger
	upstream *url.URL
	metrics  Metrics

	http     *http.Server
	tr       *http.Transport
	listener net.Listener

	// manualRelay is set when a relay was pinned by hand rather than chosen
	// from a directory, so status can name it.
	manualRelay string
}

// New validates a Config and returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("bearer: Addr is required")
	}
	if cfg.Dialer == nil {
		return nil, errors.New("bearer: Dialer is required")
	}
	if cfg.Upstream == "" {
		cfg.Upstream = DefaultUpstream
	}
	if cfg.ResponseHeaderTimeout <= 0 {
		cfg.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	up, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, fmt.Errorf("bearer: parsing upstream %q: %w", cfg.Upstream, err)
	}
	if up.Scheme != "https" {
		return nil, fmt.Errorf("bearer: upstream must be https, got %q; a plaintext upstream would let the relay read every prompt", cfg.Upstream)
	}
	if up.Host == "" {
		return nil, fmt.Errorf("bearer: upstream %q has no host", cfg.Upstream)
	}

	if !cfg.AllowNonLoopback {
		if err := requireLoopback(cfg.Addr); err != nil {
			return nil, err
		}
	}

	s := &Server{cfg: cfg, log: cfg.Logger, upstream: up, manualRelay: cfg.ManualRelay}
	s.tr = s.transport()

	proxy := &httputil.ReverseProxy{
		Rewrite:   s.rewrite,
		Transport: s.tr,
		// -1 flushes each write immediately, which is what makes server-sent
		// events arrive token by token. Any buffering here would turn a
		// streaming response into one long pause followed by a wall of text.
		FlushInterval: -1,
		ErrorHandler:  s.handleError,
	}

	var handler http.Handler = proxy
	if cfg.Tokens != nil {
		handler = s.withToken(proxy)
	}

	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.routes(handler),
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
	}
	return s, nil
}

// withToken takes payment before proxying.
//
// A token is acquired here rather than inside rewrite because rewrite cannot
// report a failure: it has no way to answer the request, so a mint being
// unreachable would surface as a request forwarded without payment.
func (s *Server) withToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := s.cfg.Tokens.Take(r.Context())
		if err != nil {
			s.metrics.NoToken.Add(1)
			s.log.Error("could not get a token", "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"osanwe_no_token","message":%q}}`+"\n",
				"Could not buy a token: "+err.Error())
			return
		}

		tracked := &tokenTracker{token: tok}
		r = r.WithContext(context.WithValue(r.Context(), tokenKey{}, tracked))
		next.ServeHTTP(w, r)

		// If the request never made it far enough to hand the token over, it
		// is still good and goes back in the wallet.
		if !tracked.presented {
			s.cfg.Tokens.Put(tok)
		}
	})
}

// tokenTracker records whether the token actually reached the gateway.
type tokenTracker struct {
	token     *mint.Token
	presented bool
}

// transport dials every upstream connection through the tunnel and runs TLS to
// the provider over it. The provider's certificate is verified normally: the
// tunnel authenticates the relay, and this authenticates the provider, and
// neither substitutes for the other.
func (s *Server) transport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return s.cfg.Dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			ServerName: hostOnly(s.upstream.Host),
			MinVersion: tls.VersionTLS12,
			RootCAs:    s.cfg.UpstreamRootCAs,
		},
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: s.cfg.ResponseHeaderTimeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
	}
}

// rewrite points the request at the upstream and strips anything that would
// describe the local machine.
func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	s.metrics.Requests.Add(1)

	pr.Out.URL.Scheme = s.upstream.Scheme
	pr.Out.URL.Host = s.upstream.Host
	pr.Out.Host = s.upstream.Host

	if base := strings.TrimSuffix(s.upstream.Path, "/"); base != "" {
		pr.Out.URL.Path = base + pr.Out.URL.Path
	}

	// Deliberately not calling SetXForwarded. The whole point is that the
	// provider does not learn where the request came from, and adding
	// X-Forwarded-For would hand it a client address -- useless here, since it
	// is always a loopback address, but the habit is exactly wrong and a
	// future edit that binds a routable address would turn it into a real leak.
	pr.Out.Header.Del("X-Forwarded-For")
	pr.Out.Header.Del("X-Forwarded-Host")
	pr.Out.Header.Del("X-Forwarded-Proto")
	pr.Out.Header.Del("Forwarded")

	// Proxy credentials are for the relay and were consumed by the tunnel;
	// they must never reach the provider.
	pr.Out.Header.Del("Proxy-Authorization")
	pr.Out.Header.Del("Proxy-Connection")

	if tracked, ok := pr.In.Context().Value(tokenKey{}).(*tokenTracker); ok {
		// Paying with a token means the user has no account in play, so
		// anything credential-shaped their tool attached is dropped here
		// rather than at the gateway. A key that never leaves the machine
		// cannot be logged by a gateway, leaked by one, or subpoenaed from
		// one.
		pr.Out.Header.Del("Authorization")
		pr.Out.Header.Del("X-Api-Key")
		pr.Out.Header.Del("Api-Key")

		pr.Out.Header.Set(gatewayTokenHeader, tracked.token.Encode())
		tracked.presented = true
	}
}

// handleError reports a failure without leaking request content into logs.
func (s *Server) handleError(w http.ResponseWriter, r *http.Request, err error) {
	s.metrics.TunnelFails.Add(1)
	// The path is logged; the body never is. A client that logged prompts
	// would recreate on the user's disk the record the network avoids
	// creating anywhere else.
	s.log.Error("upstream request failed", "path", r.URL.Path, "error", err)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, `{"type":"error","error":{"type":"osanwe_tunnel_error","message":%q}}`+"\n", err.Error())
}

// Metrics returns the server's counters.
func (s *Server) Metrics() *Metrics { return &s.metrics }

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
		return fmt.Errorf("bearer: listening on %s: %w", s.cfg.Addr, err)
	}
	s.listener = ln
	return nil
}

// Serve accepts requests until shutdown. It calls Listen if needed.
func (s *Server) Serve() error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the server, waiting for in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// requireLoopback refuses an address that is not on the loopback interface.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bearer: Addr %q must be host:port: %w", addr, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		return fmt.Errorf("bearer: refusing to bind %q, which listens on every interface. "+
			"Traffic between your tools and bearer is plaintext; exposing it would put prompts on the network in the clear. "+
			"Use a loopback address, or explicitly opt in to exposed binding if you have arranged your own protection", addr)
	case "localhost":
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("bearer: cannot tell whether %q is loopback; use 127.0.0.1", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("bearer: refusing to bind non-loopback address %q. "+
			"Traffic between your tools and bearer is plaintext; exposing it would put prompts on the network in the clear. "+
			"Use a loopback address, or explicitly opt in to exposed binding if you have arranged your own protection", host)
	}
	return nil
}

// hostOnly strips a port if present.
func hostOnly(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// UpstreamAddr returns the "host:port" bearer will tunnel to, which is what an
// operator must add to their relay's allowlist.
func (s *Server) UpstreamAddr() string {
	host := s.upstream.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		return net.JoinHostPort(host, "443")
	}
	return host
}
