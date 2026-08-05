// Package tunnel dials a provider through a ranger.
//
// The result is a net.Conn carrying raw bytes to the destination, over which
// the caller runs its own TLS session. That layering is the whole point: the
// relay sees a TLS handshake it has no key for, so it can carry the traffic
// without being able to read it.
//
// Two encryption layers are therefore in play, and they protect different
// things. The outer one, client to relay, hides the CONNECT target so an
// observer on the client's uplink cannot see which provider is being used. The
// inner one, client to provider, hides the prompt from the relay. Dropping
// either would leave a real gap.
package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/certs"
)

// DefaultTimeout bounds relay connection and CONNECT negotiation. It does not
// bound the tunnel's lifetime once established.
const DefaultTimeout = 15 * time.Second

// Config describes the relay to dial through.
type Config struct {
	// Relay is the ranger's "host:port".
	Relay string

	// Pin is the ranger's expected public-key fingerprint. Required: without
	// it there is nothing to authenticate the relay, and an attacker who can
	// redirect the connection could present their own relay and learn which
	// provider is being used.
	Pin string

	// Secret authenticates the client to the relay.
	Secret string

	// Timeout bounds dialling and negotiation. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Dialer opens tunnels through one ranger.
type Dialer struct {
	cfg     Config
	tlsConf *tls.Config
	netDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// New validates a Config and returns a Dialer.
func New(cfg Config) (*Dialer, error) {
	if cfg.Relay == "" {
		return nil, errors.New("tunnel: Relay is required")
	}
	if _, _, err := net.SplitHostPort(cfg.Relay); err != nil {
		return nil, fmt.Errorf("tunnel: Relay %q must be host:port: %w", cfg.Relay, err)
	}
	if cfg.Secret == "" {
		return nil, errors.New("tunnel: Secret is required")
	}
	if cfg.Pin == "" {
		return nil, errors.New("tunnel: Pin is required; without it the relay is unauthenticated and could be substituted")
	}
	wantPin, err := certs.NormalizePin(cfg.Pin)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}

	return &Dialer{
		cfg: cfg,
		tlsConf: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Verification is by pin, below. Name and chain verification are
			// meaningless for a relay on a bare IP with a self-signed
			// identity, and a pin is a stronger statement than either: it
			// names one specific key rather than trusting any CA to vouch.
			InsecureSkipVerify:    true,
			VerifyPeerCertificate: pinVerifier(wantPin),
		},
		netDial: (&net.Dialer{Timeout: cfg.Timeout}).DialContext,
	}, nil
}

// pinVerifier returns a callback accepting only a leaf whose public key
// matches the expected fingerprint.
func pinVerifier(want string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("tunnel: relay presented no certificate")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("tunnel: parsing relay certificate: %w", err)
		}
		if got := certs.Pin(leaf); got != want {
			return fmt.Errorf("tunnel: relay key mismatch\n  expected %s\n  got      %s\nthe relay's identity changed, or something is impersonating it", want, got)
		}
		return nil
	}
}

// DialContext opens a tunnel to addr ("host:port") through the relay.
//
// The returned conn carries raw bytes to the destination; the caller is
// expected to start its own TLS session over it.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("tunnel: unsupported network %q", network)
	}

	raw, err := d.netDial(ctx, "tcp", d.cfg.Relay)
	if err != nil {
		return nil, fmt.Errorf("tunnel: connecting to relay %s: %w", d.cfg.Relay, err)
	}

	conn := tls.Client(raw, d.tlsConf)

	// Apply the deadline to the handshake and the CONNECT exchange, then clear
	// it. A deadline left in place would later kill a long-lived stream.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(d.cfg.Timeout))
	}

	if err := conn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tunnel: TLS handshake with relay: %w", err)
	}

	if err := d.negotiate(conn, addr); err != nil {
		conn.Close()
		return nil, err
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tunnel: clearing deadline: %w", err)
	}
	return conn, nil
}

// negotiate performs the CONNECT exchange.
func (d *Dialer) negotiate(conn net.Conn, addr string) error {
	// With method CONNECT and an empty path, net/http writes the request line
	// in authority form ("CONNECT host:port HTTP/1.1"), which is what the
	// method requires. Leaving Path empty is what selects that behaviour.
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &neturl.URL{Host: addr},
		Host:   addr,
		Header: http.Header{
			"Proxy-Authorization": {auth.Header(d.cfg.Secret)},
			"User-Agent":          {"osanwe-bearer"},
		},
	}
	if err := req.Write(conn); err != nil {
		return fmt.Errorf("tunnel: sending CONNECT: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fmt.Errorf("tunnel: reading CONNECT response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &Error{Status: resp.StatusCode, Target: addr, Relay: d.cfg.Relay}
	}

	// http.ReadResponse may have buffered bytes past the response head. For a
	// CONNECT the server does not speak first, so anything buffered means the
	// relay is misbehaving; carrying on would silently drop those bytes.
	if n := br.Buffered(); n > 0 {
		return fmt.Errorf("tunnel: relay sent %d unexpected bytes after the CONNECT response", n)
	}
	return nil
}

// Error describes a relay's refusal, translated into something an operator can
// act on rather than a bare status code.
type Error struct {
	Status int
	Target string
	Relay  string
}

func (e *Error) Error() string {
	switch e.Status {
	case http.StatusProxyAuthRequired:
		return fmt.Sprintf("relay %s rejected the credential (407). Check the secret matches the one the relay was started with", e.Relay)
	case http.StatusForbidden:
		return fmt.Sprintf("relay %s will not carry traffic to %s (403). Its operator must add that destination with -allow", e.Relay, e.Target)
	case http.StatusBadGateway:
		return fmt.Sprintf("relay %s could not reach %s (502). The destination may be down, or the relay may have no route to it", e.Relay, e.Target)
	case http.StatusMethodNotAllowed:
		return fmt.Sprintf("%s does not appear to be an osanwe ranger (405)", e.Relay)
	default:
		return fmt.Sprintf("relay %s refused the tunnel to %s with status %d", e.Relay, e.Target, e.Status)
	}
}

// Hostname reports the host without its port, for callers that need it.
func Hostname(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return strings.TrimSpace(hostPort)
}
