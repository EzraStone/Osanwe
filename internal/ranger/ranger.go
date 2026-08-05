// Package ranger implements the relay node.
//
// A ranger carries an encrypted tunnel between a bearer and a provider. It is
// deliberately incapable of reading what it carries: the client's TLS session
// terminates at the provider, so everything crossing the relay is ciphertext
// the operator has no key for. That is the property the whole design rests on,
// and it is what makes running a ranger materially less risky than running a
// Tor exit.
//
// The privacy of the people using a relay is bounded by what its operator
// records, so this package treats logging as a security surface rather than a
// convenience. See the comment on Config.LogDestinations.
package ranger

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/policy"
)

// Default timeouts. These bound how long a stalled peer can hold resources
// without cutting off a legitimately slow model response: an LLM can think for
// a long time before emitting its first token, so IdleTimeout is generous.
const (
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultDialTimeout      = 10 * time.Second
	DefaultIdleTimeout      = 10 * time.Minute
)

// Config configures a Server. Addr, TLS, Allowlist and Auth are required.
type Config struct {
	Addr      string
	TLS       *tls.Config
	Allowlist *policy.Allowlist
	Auth      *auth.Authenticator

	HandshakeTimeout time.Duration
	DialTimeout      time.Duration
	IdleTimeout      time.Duration

	Logger *slog.Logger

	// LogDestinations records which destination each connection asked for.
	//
	// Off by default, and that default is a security decision rather than a
	// tidiness one. A relay that records "this address, at this time, talked
	// to this provider" is building exactly the correlation log the network
	// exists to prevent, and a seized or subpoenaed relay would hand it over.
	// The operator gains almost nothing: destinations are already constrained
	// to a short allowlist, so the log answers a question anyone could guess.
	//
	// Turn it on to debug a misconfigured allowlist, then turn it off.
	LogDestinations bool

	// dialContext is overridden by tests to avoid real network access.
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Metrics are cumulative counters. They deliberately hold no per-connection
// detail: aggregate counts let an operator see that their relay is working
// without accumulating anything worth seizing.
type Metrics struct {
	Accepted      atomic.Int64
	AuthFailed    atomic.Int64
	PolicyDenied  atomic.Int64
	BadRequest    atomic.Int64
	DialFailed    atomic.Int64
	Tunnels       atomic.Int64
	TunnelsActive atomic.Int64
	BytesToClient atomic.Int64
	BytesToTarget atomic.Int64
}

// Server is a ranger relay.
type Server struct {
	cfg     Config
	log     *slog.Logger
	metrics Metrics

	http *http.Server

	mu       sync.Mutex
	listener net.Listener
}

// New validates a Config and returns a Server.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Addr == "":
		return nil, errors.New("ranger: Addr is required")
	case cfg.TLS == nil:
		return nil, errors.New("ranger: TLS is required; an unencrypted relay would expose the CONNECT target to anyone watching the client's uplink")
	case cfg.Allowlist == nil || cfg.Allowlist.Len() == 0:
		return nil, errors.New("ranger: a non-empty Allowlist is required")
	case cfg.Auth == nil:
		return nil, errors.New("ranger: Auth is required; an unauthenticated relay becomes someone else's abuse proxy within hours")
	}

	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.dialContext == nil {
		cfg.dialContext = (&net.Dialer{Timeout: cfg.DialTimeout}).DialContext
	}

	s := &Server{cfg: cfg, log: cfg.Logger}
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s,
		TLSConfig:         cfg.TLS,
		ReadHeaderTimeout: cfg.HandshakeTimeout,
		ErrorLog:          nil,
		// HTTP/2 cannot be hijacked, and hijacking is how a CONNECT tunnel is
		// handed off to the byte pump. Restrict to HTTP/1.1 explicitly rather
		// than relying on ALPN negotiation to happen to pick it.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	return s, nil
}

// Metrics returns the server's counters.
func (s *Server) Metrics() *Metrics { return &s.metrics }

// Addr returns the address actually being listened on, which differs from the
// configured address when port 0 was requested.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Listen binds the configured address without serving, so a caller can learn
// the assigned port before traffic starts.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("ranger: listening on %s: %w", s.cfg.Addr, err)
	}
	s.mu.Lock()
	s.listener = tls.NewListener(ln, s.cfg.TLS)
	s.mu.Unlock()
	return nil
}

// Serve accepts connections until the server is shut down. It calls Listen if
// that has not happened yet. It returns nil on a clean shutdown.
func (s *Server) Serve() error {
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
		s.mu.Lock()
		ln = s.listener
		s.mu.Unlock()
	}

	err := s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops accepting connections and waits for the context to expire.
//
// Established tunnels are hijacked connections, which http.Server does not
// track, so Close is called afterwards to tear them down. A tunnel carrying a
// half-finished model response will be cut; that is the honest behaviour for a
// relay being stopped, and better than hanging until an idle timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	_ = s.http.Close()
	return err
}

// ServeHTTP handles a request. Only CONNECT is meaningful; everything else is
// refused, because a ranger is a tunnel and not a web server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.metrics.Accepted.Add(1)

	if r.Method != http.MethodConnect {
		s.metrics.BadRequest.Add(1)
		w.Header().Set("Allow", http.MethodConnect)
		http.Error(w, "this is an osanwe ranger; it speaks CONNECT only", http.StatusMethodNotAllowed)
		return
	}

	if !s.cfg.Auth.CheckHeader(r.Header) {
		s.metrics.AuthFailed.Add(1)
		// No client address in this log line. Recording who failed to
		// authenticate would build the same correlation log the network is
		// meant to avoid, and a scanner probing every relay on the internet
		// is not interesting enough to be worth it.
		s.log.Warn("rejected connection: bad or missing credentials")
		w.Header().Set("Proxy-Authenticate", `Bearer realm="osanwe"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}

	target := r.Host // for CONNECT this is the "host:port" from the request line
	if !s.cfg.Allowlist.Allows(target) {
		s.metrics.PolicyDenied.Add(1)
		if s.cfg.LogDestinations {
			s.log.Warn("rejected connection: destination not on allowlist", "destination", target)
		} else {
			s.log.Warn("rejected connection: destination not on allowlist")
		}
		http.Error(w, "destination not permitted by this relay", http.StatusForbidden)
		return
	}

	dialCtx, cancel := context.WithTimeout(r.Context(), s.cfg.DialTimeout)
	defer cancel()

	upstream, err := s.cfg.dialContext(dialCtx, "tcp", target)
	if err != nil {
		s.metrics.DialFailed.Add(1)
		if s.cfg.LogDestinations {
			s.log.Warn("upstream dial failed", "destination", target, "error", err)
		} else {
			s.log.Warn("upstream dial failed")
		}
		http.Error(w, "cannot reach destination", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.metrics.BadRequest.Add(1)
		http.Error(w, "connection cannot be tunnelled", http.StatusInternalServerError)
		return
	}

	// Reply before hijacking. Once the connection is hijacked the ResponseWriter
	// is no longer usable, so an error after this point can only be signalled by
	// closing.
	client, buf, err := hijacker.Hijack()
	if err != nil {
		s.metrics.BadRequest.Add(1)
		return
	}
	defer client.Close()

	if _, err := buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buf.Flush(); err != nil {
		return
	}

	s.metrics.Tunnels.Add(1)
	s.metrics.TunnelsActive.Add(1)
	defer s.metrics.TunnelsActive.Add(-1)

	// A client may have pipelined bytes after the CONNECT line; those are
	// sitting in the bufio.Reader and would be lost if we read from the socket
	// directly. Drain them first.
	if n := buf.Reader.Buffered(); n > 0 {
		pending, err := buf.Reader.Peek(n)
		if err == nil {
			if _, err := upstream.Write(pending); err != nil {
				return
			}
			_, _ = buf.Reader.Discard(n)
			s.metrics.BytesToTarget.Add(int64(n))
		}
	}

	s.pump(client, upstream)
}

// pump moves bytes in both directions until either side closes, and returns
// when both directions are done.
func (s *Server) pump(client, upstream net.Conn) {
	idle := s.cfg.IdleTimeout
	var wg sync.WaitGroup
	wg.Add(2)

	copyDir := func(dst, src net.Conn, counter *atomic.Int64) {
		defer wg.Done()
		n, _ := io.Copy(&deadlineWriter{conn: dst, idle: idle}, &deadlineReader{conn: src, idle: idle})
		counter.Add(n)
		// Half-close so the peer observes EOF rather than waiting for the idle
		// timeout. A streaming response that ends cleanly should not leave the
		// other direction hanging for minutes.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.SetReadDeadline(time.Now())
		}
	}

	go copyDir(upstream, client, &s.metrics.BytesToTarget)
	go copyDir(client, upstream, &s.metrics.BytesToClient)

	wg.Wait()
}

// deadlineReader refreshes a read deadline on every read, so the timeout
// measures inactivity rather than total connection age. A long model response
// must not be cut off simply for taking a while.
type deadlineReader struct {
	conn net.Conn
	idle time.Duration
}

func (r *deadlineReader) Read(p []byte) (int, error) {
	if r.idle > 0 {
		_ = r.conn.SetReadDeadline(time.Now().Add(r.idle))
	}
	return r.conn.Read(p)
}

type deadlineWriter struct {
	conn net.Conn
	idle time.Duration
}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	if w.idle > 0 {
		_ = w.conn.SetWriteDeadline(time.Now().Add(w.idle))
	}
	return w.conn.Write(p)
}
