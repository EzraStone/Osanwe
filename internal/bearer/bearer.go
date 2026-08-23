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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
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

// gatewayTokenOutcomeHeader is authored by the TLS-authenticated configured
// gateway, which strips any provider value before answering. It is deliberately
// duplicated here so the independently deployed client and gateway packages
// stay decoupled.
const gatewayTokenOutcomeHeader = "X-Osanwe-Token-Outcome"

const (
	tokenOutcomeSpent    = "spent"
	tokenOutcomeRefunded = "refunded"
	tokenOutcomeRejected = "rejected"
	tokenOutcomeInvalid  = "invalid"

	// Token mode has one JSON API contract. Pinning these values prevents a
	// local tool from turning otherwise allowed semantic headers into stable
	// identifying metadata visible to the gateway.
	canonicalAccept           = "application/json"
	canonicalContentType      = "application/json"
	canonicalAnthropicVersion = "2023-06-01"
	APIStyleAnthropic         = "anthropic"
	APIStyleOpenAI            = "openai"
)

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

// TokenSource supplies one token per paid inference request.
// internal/mint.Wallet satisfies it; free catalog and local refusal paths do
// not call Take.
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

	// APIStyle tells the local interface which provider-compatible request
	// shape to use. It does not change arbitrary proxy traffic from existing
	// tools; it only prevents the embedded chat from guessing a provider API.
	APIStyle string

	// Models optionally limits the model names shown by the embedded chat.
	// The caller still owns a BYOK credential, so this is an onboarding and
	// disclosure boundary rather than an authorization boundary.
	Models []string

	// Dialer opens tunnels. Required.
	Dialer Dialer

	// Tokens, when set, pays for each accepted inference request with a
	// blind-signed token and strips the user's own credentials before
	// forwarding. Upstream must then be a gateway, not a provider directly: a
	// provider would reject a token and has no idea what to do with one.
	Tokens TokenSource

	// Relays, when set, lets the client report which relay it is using.
	// internal/pool satisfies it.
	Relays RelayStatus

	// UI serves the local interface at Prefix.
	//
	// It is guarded by the same origin checks as everything else, which is
	// what makes serving a page from a daemon defensible at all.
	UI bool

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
	if strings.TrimSpace(cfg.Upstream) == "" {
		if cfg.Tokens != nil {
			return nil, errors.New("bearer: token mode requires an explicit gateway -upstream; refusing to send a paid token to the default provider")
		}
		cfg.Upstream = DefaultUpstream
	}
	if cfg.APIStyle == "" {
		cfg.APIStyle = APIStyleAnthropic
	}
	if cfg.APIStyle != APIStyleAnthropic && cfg.APIStyle != APIStyleOpenAI {
		return nil, fmt.Errorf("bearer: APIStyle must be %q or %q, got %q", APIStyleAnthropic, APIStyleOpenAI, cfg.APIStyle)
	}
	seenModels := make(map[string]struct{}, len(cfg.Models))
	models := make([]string, 0, len(cfg.Models))
	for _, candidate := range cfg.Models {
		name := strings.TrimSpace(candidate)
		if name == "" || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
			return nil, fmt.Errorf("bearer: model names must be non-empty, at most 256 bytes, and contain no control line breaks")
		}
		if _, duplicate := seenModels[name]; duplicate {
			continue
		}
		seenModels[name] = struct{}{}
		models = append(models, name)
	}
	cfg.Models = models
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
	if up.User != nil || up.ForceQuery || up.RawQuery != "" || up.Fragment != "" || up.RawFragment != "" || up.RawPath != "" {
		return nil, fmt.Errorf("bearer: upstream %q must not contain user information, a query, a fragment, or an encoded path", cfg.Upstream)
	}

	if !cfg.AllowNonLoopback {
		if err := requireLoopback(cfg.Addr); err != nil {
			return nil, err
		}
	}

	s := &Server{cfg: cfg, log: cfg.Logger, upstream: up, manualRelay: cfg.ManualRelay}
	s.tr = s.transport()

	proxy := &httputil.ReverseProxy{
		Rewrite:        s.rewrite,
		Transport:      s.tr,
		ModifyResponse: s.modifyResponse,
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
		// The gateway's locally generated model catalog is free. Bypassing Take
		// is important: sending a wallet token and relying on the gateway not to
		// spend it would still remove it from the local wallet.
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" && !r.URL.ForceQuery && r.URL.RawQuery == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Token mode deliberately exposes one paid operation, not an arbitrary
		// credential-bearing provider proxy. Refuse locally so an unsupported
		// request cannot even take a token out of the wallet.
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.URL.ForceQuery || r.URL.RawQuery != "" {
			s.refusePaidSurface(w, r)
			return
		}

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
		ctx := context.WithValue(r.Context(), tokenKey{}, tracked)
		// Rewrite only prepares an outgoing request; a dial or TLS failure can
		// happen afterwards without sending one byte. WroteRequest is the
		// boundary: once it fires (even with an error, after a partial write),
		// the token may have reached and been spent by the gateway. Before it
		// fires, returning the untouched token is safe.
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			WroteRequest: func(httptrace.WroteRequestInfo) {
				tracked.presented.Store(true)
			},
		})
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)

		// If the request never made it far enough to hand the token over, it
		// is still good and goes back in the wallet.
		if !tracked.presented.Load() || tracked.reusable.Load() {
			s.cfg.Tokens.Put(tok)
		}
	})
}

// tokenTracker records whether the token left bearer and whether the pinned
// gateway explicitly proved it reusable. Atomics keep this correct if a
// transport invokes response hooks on a different goroutine.
type tokenTracker struct {
	token     *mint.Token
	presented atomic.Bool
	reusable  atomic.Bool
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

	path := pr.In.URL.Path
	pr.Out.URL.Scheme = s.upstream.Scheme
	pr.Out.URL.Opaque = ""
	pr.Out.URL.User = nil
	pr.Out.URL.Host = s.upstream.Host
	pr.Out.URL.Path = path
	pr.Out.URL.RawPath = ""
	pr.Out.URL.OmitHost = false
	pr.Out.URL.Fragment = ""
	pr.Out.URL.RawFragment = ""
	if s.cfg.Tokens != nil {
		// Token mode already refuses queries. Clear both representations here as
		// defense in depth so only the exact paid/free gateway path is visible.
		pr.Out.URL.ForceQuery = false
		pr.Out.URL.RawQuery = ""
	}
	pr.Out.Host = s.upstream.Host

	if base := strings.TrimSuffix(s.upstream.Path, "/"); base != "" {
		pr.Out.URL.Path = base + path
	}

	// Keep only fields that change the API meaning. A positive allowlist drops
	// caller addresses, cookies, browser hints, SDK/runtime versions, tracing
	// ids and arbitrary custom metadata without having to predict every name.
	allowed := []string{"Accept", "Content-Type", "Anthropic-Version"}
	if s.cfg.Tokens == nil {
		// BYOK has to preserve whichever standard credential shape the tool
		// uses. Those are the only additional caller headers allowed out.
		allowed = append(allowed, "Authorization", "X-Api-Key", "Api-Key")
	}
	pr.Out.Header = allowedHeaders(pr.Out.Header, allowed...)
	// Trailer fields are carried outside Header and would otherwise bypass the
	// allowlist on a chunked request. The transport may choose chunked framing
	// again when the body length is unknown, but caller-supplied trailer names
	// and values must not cross this privacy boundary.
	pr.Out.Trailer = nil
	pr.Out.TransferEncoding = nil
	if s.cfg.Tokens != nil {
		// The anonymous path has one protocol version and one JSON request
		// shape. Do not forward caller-selected parameters or version strings:
		// those are enough to act as a persistent fingerprint even though the
		// header names themselves are legitimate API fields.
		pr.Out.Header.Set("Accept", canonicalAccept)
		if pr.In.Method == http.MethodPost && pr.In.URL.Path == "/v1/messages" {
			pr.Out.Header.Set("Content-Type", canonicalContentType)
			pr.Out.Header.Set("Anthropic-Version", canonicalAnthropicVersion)
		} else {
			pr.Out.Header.Del("Content-Type")
			pr.Out.Header.Del("Anthropic-Version")
		}
	}
	// Suppress net/http's automatic Go-http-client/1.1; replacing the caller's
	// fingerprint with bearer's runtime fingerprint would still be a leak.
	pr.Out.Header["User-Agent"] = []string{""}

	if tracked, ok := pr.In.Context().Value(tokenKey{}).(*tokenTracker); ok {
		pr.Out.Header.Set(gatewayTokenHeader, tracked.token.Encode())
	}
}

func allowedHeaders(source http.Header, names ...string) http.Header {
	kept := make(http.Header, len(names)+1)
	for _, name := range names {
		if values, ok := source[http.CanonicalHeaderKey(name)]; ok {
			kept[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return kept
}

// modifyResponse applies only a result asserted by the authenticated gateway.
// Missing and unknown values fail closed for compatibility with old gateways:
// bearer does not put the token back and risk a replay loop.
func (s *Server) modifyResponse(resp *http.Response) error {
	tracked, ok := resp.Request.Context().Value(tokenKey{}).(*tokenTracker)
	trustedOutcome := ""
	if ok {
		switch outcome := resp.Header.Get(gatewayTokenOutcomeHeader); outcome {
		case tokenOutcomeRefunded, tokenOutcomeRejected:
			tracked.reusable.Store(true)
			trustedOutcome = outcome
		case tokenOutcomeSpent, tokenOutcomeInvalid:
			// Intentionally retained as spent locally.
			trustedOutcome = outcome
		case "":
			s.log.Warn("gateway response had no token outcome; retaining token as spent for safety")
		default:
			s.log.Warn("gateway response had an unknown token outcome; retaining token as spent for safety",
				"outcome", outcome)
		}
	}

	stripBearerResponseHeaders(resp)
	if ok && trustedOutcome != "" {
		resp.Header.Set(gatewayTokenOutcomeHeader, trustedOutcome)
	} else {
		resp.Header.Del(gatewayTokenOutcomeHeader)
	}
	if redirectsAutomatically(resp.StatusCode) {
		replaceRedirectResponse(resp,
			"The upstream attempted to redirect this request. Redirects are blocked because following one from this machine would bypass the anonymity relay.")
		if trustedOutcome != "" {
			resp.Header.Set(gatewayTokenOutcomeHeader, trustedOutcome)
		}
	}
	return nil
}

func stripBearerResponseHeaders(resp *http.Response) {
	for _, name := range []string{
		"Authorization", "X-Api-Key", "Api-Key", gatewayTokenHeader,
		"Location", "Refresh", "Link", "Alt-Svc", "Set-Cookie",
		"Report-To", "Reporting-Endpoints", "NEL", "Expect-CT",
		"Public-Key-Pins", "Public-Key-Pins-Report-Only",
		"Content-Security-Policy", "Content-Security-Policy-Report-Only",
	} {
		resp.Header.Del(name)
	}
	for name := range resp.Header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			resp.Header.Del(name)
			continue
		}
		for _, value := range resp.Header.Values(name) {
			for _, secretName := range []string{"Authorization", "X-Api-Key", "Api-Key", gatewayTokenHeader} {
				for _, secret := range resp.Request.Header.Values(secretName) {
					if secret != "" && strings.TrimSpace(value) == secret {
						resp.Header.Del(name)
					}
				}
			}
		}
	}
	resp.Header.Del("Trailer")
	resp.Trailer = nil
	resp.TransferEncoding = nil
	setPrivateResponseHeaders(resp.Header)
}

func setPrivateResponseHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
}

func redirectsAutomatically(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func replaceRedirectResponse(resp *http.Response, message string) {
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	body := []byte(fmt.Sprintf(`{"type":"error","error":{"type":"osanwe_redirect_blocked","message":%q}}`+"\n", message))
	resp.StatusCode = http.StatusBadGateway
	resp.Status = fmt.Sprintf("%d %s", http.StatusBadGateway, http.StatusText(http.StatusBadGateway))
	resp.Header = make(http.Header)
	resp.Header.Set("Content-Type", "application/json")
	setPrivateResponseHeaders(resp.Header)
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.TransferEncoding = nil
	resp.Trailer = nil
}

func (s *Server) refusePaidSurface(w http.ResponseWriter, r *http.Request) {
	status := http.StatusNotFound
	kind := "unsupported_endpoint"
	message := "Token mode exposes only GET /v1/models and POST /v1/messages. No token was taken."
	if (r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/models") && (r.URL.ForceQuery || r.URL.RawQuery != "") {
		status = http.StatusBadRequest
		kind = "query_not_allowed"
	} else if r.URL.Path == "/v1/messages" && r.Method != http.MethodPost {
		status = http.StatusMethodNotAllowed
		kind = "method_not_allowed"
		w.Header().Set("Allow", http.MethodPost)
	}
	w.Header().Set("Content-Type", "application/json")
	setPrivateResponseHeaders(w.Header())
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`+"\n",
		"osanwe_"+kind, message)
}

// handleError reports a failure without leaking request content into logs.
func (s *Server) handleError(w http.ResponseWriter, r *http.Request, err error) {
	s.metrics.TunnelFails.Add(1)
	// The path is logged; the body never is. A client that logged prompts
	// would recreate on the user's disk the record the network avoids
	// creating anywhere else.
	s.log.Error("upstream request failed", "path", r.URL.Path, "error", err)

	message, _ := explain(err)
	w.Header().Set("Content-Type", "application/json")
	setPrivateResponseHeaders(w.Header())
	w.WriteHeader(http.StatusBadGateway)
	// Both, deliberately: the sentence is for whoever is looking at a screen,
	// and detail is the original for whoever is debugging a relay.
	fmt.Fprintf(w, `{"type":"error","error":{"type":"osanwe_tunnel_error","message":%q,"detail":%q}}`+"\n",
		message, err.Error())
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
