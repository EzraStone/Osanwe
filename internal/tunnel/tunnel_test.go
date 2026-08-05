package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/policy"
	"github.com/EzraStone/osanwe/internal/ranger"
)

const secret = "0123456789abcdef0123456789abcdef"

// fixture brings up an echo upstream and a ranger allowed to reach it.
type fixture struct {
	relayAddr string
	pin       string
	echoAddr  string
}

func newFixture(t *testing.T, allow ...string) fixture {
	t.Helper()

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()

	if len(allow) == 0 {
		allow = []string{echo.Addr().String()}
	}

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
	srv, err := ranger.New(ranger.Config{
		Addr:      "127.0.0.1:0",
		TLS:       &tls.Config{Certificates: []tls.Certificate{cert}},
		Allowlist: al,
		Auth:      au,
	})
	if err != nil {
		t.Fatalf("ranger.New: %v", err)
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

	return fixture{relayAddr: srv.Addr().String(), pin: pin, echoAddr: echo.Addr().String()}
}

func TestNewValidatesConfig(t *testing.T) {
	base := Config{Relay: "127.0.0.1:8443", Pin: "sha256/" + strings.Repeat("A", 43) + "=", Secret: secret}

	cases := map[string]func(c *Config){
		"no relay":      func(c *Config) { c.Relay = "" },
		"relay no port": func(c *Config) { c.Relay = "example.com" },
		"no secret":     func(c *Config) { c.Secret = "" },
		"no pin":        func(c *Config) { c.Pin = "" },
		"bad pin":       func(c *Config) { c.Pin = "not-a-pin" },
		"short pin":     func(c *Config) { c.Pin = "sha256/AAAA" },
	}
	for name, mutate := range cases {
		cfg := base
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("New with %s succeeded, want error", name)
		}
	}

	if _, err := New(base); err != nil {
		t.Errorf("New with a valid config failed: %v", err)
	}
}

func TestDialCarriesTrafficThroughRelay(t *testing.T) {
	f := newFixture(t)

	d, err := New(Config{Relay: f.relayAddr, Pin: f.pin, Secret: secret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn, err := d.DialContext(context.Background(), "tcp", f.echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	const payload = "through the tunnel"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != payload {
		t.Errorf("got %q, want %q", got, payload)
	}
}

func TestDialRejectsWrongPin(t *testing.T) {
	f := newFixture(t)

	// A syntactically valid pin belonging to a different key.
	_, otherPin, err := certs.SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}

	d, err := New(Config{Relay: f.relayAddr, Pin: otherPin, Secret: secret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.DialContext(context.Background(), "tcp", f.echoAddr); err == nil {
		t.Fatal("dial succeeded against a relay whose key did not match the pin")
	} else if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %v, want a key-mismatch message", err)
	}
}

func TestDialSurfacesRelayRefusals(t *testing.T) {
	f := newFixture(t)

	t.Run("bad credential", func(t *testing.T) {
		d, err := New(Config{Relay: f.relayAddr, Pin: f.pin, Secret: "wrong-but-long-enough-secret-here"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, err = d.DialContext(context.Background(), "tcp", f.echoAddr)
		var te *Error
		if !errors.As(err, &te) {
			t.Fatalf("error = %v, want *tunnel.Error", err)
		}
		if te.Status != http.StatusProxyAuthRequired {
			t.Errorf("status = %d, want 407", te.Status)
		}
		if !strings.Contains(te.Error(), "secret") {
			t.Errorf("message %q does not tell the operator to check the secret", te.Error())
		}
	})

	t.Run("destination not allowed", func(t *testing.T) {
		d, err := New(Config{Relay: f.relayAddr, Pin: f.pin, Secret: secret})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, err = d.DialContext(context.Background(), "tcp", "not-allowed.example:443")
		var te *Error
		if !errors.As(err, &te) {
			t.Fatalf("error = %v, want *tunnel.Error", err)
		}
		if te.Status != http.StatusForbidden {
			t.Errorf("status = %d, want 403", te.Status)
		}
		if !strings.Contains(te.Error(), "-allow") {
			t.Errorf("message %q does not say how to fix the problem", te.Error())
		}
	})
}

func TestDialRejectsNonTCPNetworks(t *testing.T) {
	f := newFixture(t)
	d, err := New(Config{Relay: f.relayAddr, Pin: f.pin, Secret: secret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.DialContext(context.Background(), "udp", f.echoAddr); err == nil {
		t.Error("DialContext accepted a udp network")
	}
}

func TestDialHonoursContextCancellation(t *testing.T) {
	f := newFixture(t)
	d, err := New(Config{Relay: f.relayAddr, Pin: f.pin, Secret: secret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.DialContext(ctx, "tcp", f.echoAddr); err == nil {
		t.Error("DialContext ignored a cancelled context")
	}
}

func TestEstablishedTunnelHasNoDeadline(t *testing.T) {
	// A deadline left over from negotiation would kill a long-lived stream
	// mid-response, which is exactly what an LLM connection looks like.
	f := newFixture(t)
	d, err := New(Config{Relay: f.relayAddr, Pin: f.pin, Secret: secret, Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn, err := d.DialContext(context.Background(), "tcp", f.echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	// Idle well past the negotiation timeout, then confirm the tunnel works.
	time.Sleep(600 * time.Millisecond)

	if _, err := conn.Write([]byte("still here")); err != nil {
		t.Fatalf("write after idling past the negotiation timeout: %v", err)
	}
	got := make([]byte, len("still here"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read after idling past the negotiation timeout: %v", err)
	}
	if string(got) != "still here" {
		t.Errorf("got %q, want %q", got, "still here")
	}
}

func TestHostname(t *testing.T) {
	for in, want := range map[string]string{
		"api.anthropic.com:443": "api.anthropic.com",
		"api.anthropic.com":     "api.anthropic.com",
		"[::1]:8443":            "::1",
		"  spaced.example  ":    "spaced.example",
	} {
		if got := Hostname(in); got != want {
			t.Errorf("Hostname(%q) = %q, want %q", in, got, want)
		}
	}
}
