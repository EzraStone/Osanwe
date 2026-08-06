package ranger

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/policy"
)

const secret = "0123456789abcdef0123456789abcdef"

// echoServer is a stand-in upstream that echoes what it receives.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

// startRanger brings up a relay allowing exactly the given destinations.
func startRanger(t *testing.T, allow []string) (*Server, string, string) {
	t.Helper()

	cert, pin, err := certs.SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	al, err := policy.Parse(allow)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	au, err := auth.New(secret)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	srv, err := New(Config{
		Addr:      "127.0.0.1:0",
		TLS:       &tls.Config{Certificates: []tls.Certificate{cert}},
		Allowlist: al,
		Auth:      au,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	return srv, srv.Addr().String(), pin
}

// dialRanger opens a TLS connection to the relay, verifying by pin.
func dialRanger(t *testing.T, addr, wantPin string) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if got := certs.Pin(leaf); got != wantPin {
				return fmt.Errorf("pin mismatch: %s != %s", got, wantPin)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("dial ranger: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// connect issues a CONNECT and returns the status line.
func connect(t *testing.T, conn net.Conn, target, credential string) (string, *bufio.Reader) {
	t.Helper()
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if credential != "" {
		req += "Proxy-Authorization: " + credential + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	// Drain the header block. The blank line terminating it is part of the
	// response, and leaving it in the buffer would corrupt the first read of
	// tunnelled data.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	return strings.TrimSpace(status), br
}

func TestNewRejectsUnsafeConfigs(t *testing.T) {
	cert, _, _ := certs.SelfSigned([]string{"localhost"}, time.Hour)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	al, _ := policy.Parse([]string{"example.com:443"})
	au, _ := auth.New(secret)

	cases := map[string]Config{
		"no addr":      {TLS: tlsCfg, Allowlist: al, Auth: au},
		"no TLS":       {Addr: "127.0.0.1:0", Allowlist: al, Auth: au},
		"no allowlist": {Addr: "127.0.0.1:0", TLS: tlsCfg, Auth: au},
		"no auth":      {Addr: "127.0.0.1:0", TLS: tlsCfg, Allowlist: al},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("New with %s succeeded; unsafe configurations must be refused at construction", name)
		}
	}
}

func TestTunnelCarriesTrafficEndToEnd(t *testing.T) {
	echo := echoServer(t)
	_, addr, pin := startRanger(t, []string{echo.Addr().String()})

	conn := dialRanger(t, addr, pin)
	status, br := connect(t, conn, echo.Addr().String(), auth.Header(secret))
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status = %q, want 200", status)
	}

	const payload = "hello through the relay"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != payload {
		t.Errorf("echoed %q, want %q", got, payload)
	}
}

func TestRejectsMissingAndBadCredentials(t *testing.T) {
	echo := echoServer(t)
	srv, addr, pin := startRanger(t, []string{echo.Addr().String()})

	for _, cred := range []string{"", "Bearer wrong", "Basic bm90OnJpZ2h0"} {
		conn := dialRanger(t, addr, pin)
		status, _ := connect(t, conn, echo.Addr().String(), cred)
		if !strings.Contains(status, "407") {
			t.Errorf("credential %q: status = %q, want 407", cred, status)
		}
	}
	if got := srv.Metrics().AuthFailed.Load(); got != 3 {
		t.Errorf("AuthFailed = %d, want 3", got)
	}
	if got := srv.Metrics().Tunnels.Load(); got != 0 {
		t.Errorf("Tunnels = %d, want 0; no tunnel may be established without credentials", got)
	}
}

func TestRejectsDestinationsOffTheAllowlist(t *testing.T) {
	echo := echoServer(t)
	srv, addr, pin := startRanger(t, []string{echo.Addr().String()})

	for _, target := range []string{
		"evil.example.com:443",
		"127.0.0.1:9", // right host family, unlisted port
		strings.TrimSuffix(echo.Addr().String(), ":"+port(echo)) + ":1", // listed host, wrong port
	} {
		conn := dialRanger(t, addr, pin)
		status, _ := connect(t, conn, target, auth.Header(secret))
		if !strings.Contains(status, "403") {
			t.Errorf("target %q: status = %q, want 403", target, status)
		}
	}
	if got := srv.Metrics().PolicyDenied.Load(); got != 3 {
		t.Errorf("PolicyDenied = %d, want 3", got)
	}
}

func TestPolicyIsCheckedEvenWithValidCredentials(t *testing.T) {
	// Authentication must not imply authorisation: a legitimate user still
	// cannot make the relay dial arbitrary hosts.
	echo := echoServer(t)
	_, addr, pin := startRanger(t, []string{echo.Addr().String()})

	conn := dialRanger(t, addr, pin)
	status, _ := connect(t, conn, "attacker.example:443", auth.Header(secret))
	if !strings.Contains(status, "403") {
		t.Fatalf("status = %q, want 403 for an authenticated but unauthorised destination", status)
	}
}

func TestNonConnectMethodsAreRefused(t *testing.T) {
	echo := echoServer(t)
	srv, addr, pin := startRanger(t, []string{echo.Addr().String()})

	conn := dialRanger(t, addr, pin)
	req := "GET /metrics HTTP/1.1\r\nHost: relay\r\nProxy-Authorization: " + auth.Header(secret) + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405; a ranger is a tunnel, not a web server", resp.StatusCode)
	}
	if got := srv.Metrics().BadRequest.Load(); got == 0 {
		t.Error("BadRequest counter did not move")
	}
}

func TestUnreachableUpstreamReports502(t *testing.T) {
	// Bind then immediately close, so the address is allowlisted but dead.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()

	srv, addr, pin := startRanger(t, []string{deadAddr})
	conn := dialRanger(t, addr, pin)
	status, _ := connect(t, conn, deadAddr, auth.Header(secret))
	if !strings.Contains(status, "502") {
		t.Errorf("status = %q, want 502", status)
	}
	if got := srv.Metrics().DialFailed.Load(); got != 1 {
		t.Errorf("DialFailed = %d, want 1", got)
	}
}

func TestMetricsCountBytesInBothDirections(t *testing.T) {
	echo := echoServer(t)
	srv, addr, pin := startRanger(t, []string{echo.Addr().String()})

	conn := dialRanger(t, addr, pin)
	status, br := connect(t, conn, echo.Addr().String(), auth.Header(secret))
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q", status)
	}

	payload := strings.Repeat("x", 4096)
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(br, make([]byte, len(payload))); err != nil {
		t.Fatalf("read: %v", err)
	}
	conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Metrics().BytesToTarget.Load() >= int64(len(payload)) &&
			srv.Metrics().BytesToClient.Load() >= int64(len(payload)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("byte counters did not reach %d: toTarget=%d toClient=%d",
		len(payload), srv.Metrics().BytesToTarget.Load(), srv.Metrics().BytesToClient.Load())
}

func TestActiveTunnelCountReturnsToZero(t *testing.T) {
	echo := echoServer(t)
	srv, addr, pin := startRanger(t, []string{echo.Addr().String()})

	conn := dialRanger(t, addr, pin)
	if status, _ := connect(t, conn, echo.Addr().String(), auth.Header(secret)); !strings.Contains(status, "200") {
		t.Fatalf("status = %q", status)
	}
	conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Metrics().TunnelsActive.Load() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("TunnelsActive = %d after close, want 0; tunnels are leaking", srv.Metrics().TunnelsActive.Load())
}

func TestServerRefusesPlaintextClients(t *testing.T) {
	// A plaintext client must not be able to speak to the relay at all: the
	// CONNECT target would otherwise be readable on the wire.
	echo := echoServer(t)
	_, addr, _ := startRanger(t, []string{echo.Addr().String()})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := "CONNECT " + echo.Addr().String() + " HTTP/1.1\r\nProxy-Authorization: " + auth.Header(secret) + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return // a refused write is an acceptable outcome
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if n > 0 && strings.Contains(string(buf[:n]), "200") {
		t.Fatal("relay established a tunnel for a plaintext client")
	}
}

func port(ln net.Listener) string {
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	return p
}

// TestByteCountersUpdateWhileATunnelIsOpen guards observability that a
// streaming workload depends on. Counting only when io.Copy returns would show
// an operator zero traffic on a relay that is busy right now, because an LLM
// response can hold one tunnel open for minutes.
func TestByteCountersUpdateWhileATunnelIsOpen(t *testing.T) {
	echo := echoServer(t)
	srv, addr, pin := startRanger(t, []string{echo.Addr().String()})

	conn := dialRanger(t, addr, pin)
	status, br := connect(t, conn, echo.Addr().String(), auth.Header(secret))
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q", status)
	}

	payload := strings.Repeat("y", 2048)
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(br, make([]byte, len(payload))); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Deliberately do NOT close the tunnel. The counters must already reflect
	// the traffic that has crossed it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Metrics().BytesToTarget.Load() >= int64(len(payload)) &&
			srv.Metrics().BytesToClient.Load() >= int64(len(payload)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("counters still toTarget=%d toClient=%d on an open tunnel that carried %d bytes each way",
		srv.Metrics().BytesToTarget.Load(), srv.Metrics().BytesToClient.Load(), len(payload))
}
