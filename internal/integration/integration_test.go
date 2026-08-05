// Package integration wires bearer, tunnel and ranger together against a
// stand-in provider and checks the properties the design actually claims.
//
// The central one is relay blindness: a ranger must be incapable of reading
// what it carries. Design document §14 lists that as the claim users care most
// about and the easiest to demonstrate convincingly, so it is demonstrated
// here rather than asserted -- every byte crossing the relay is captured and
// searched for the prompt and the API key.
package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/bearer"
	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/policy"
	"github.com/EzraStone/osanwe/internal/ranger"
	"github.com/EzraStone/osanwe/internal/tunnel"
)

const (
	secret = "0123456789abcdef0123456789abcdef"

	// Distinctive strings. If either appears in the bytes the relay handled,
	// the core claim is false.
	promptMarker = "PROMPT-c2f6a1b4-the-quick-brown-fox-secret-question"
	apiKeyMarker = "sk-ant-APIKEY-9f3e7d1c-must-never-be-visible"
	replyMarker  = "REPLY-8a4c2e6f-model-answer-text"
)

// tap is a TCP proxy that records everything crossing it, standing in for a
// packet capture taken on the relay host.
type tap struct {
	ln net.Listener

	mu       sync.Mutex
	captured bytes.Buffer
}

func newTap(t *testing.T, upstream string) *tap {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tap listen: %v", err)
	}
	tp := &tap{ln: ln}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				server, err := net.Dial("tcp", upstream)
				if err != nil {
					return
				}
				defer server.Close()

				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _, _ = io.Copy(server, io.TeeReader(client, tp)) }()
				go func() { defer wg.Done(); _, _ = io.Copy(client, io.TeeReader(server, tp)) }()
				wg.Wait()
			}()
		}
	}()
	return tp
}

func (tp *tap) Write(p []byte) (int, error) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.captured.Write(p)
}

func (tp *tap) Bytes() []byte {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return append([]byte(nil), tp.captured.Bytes()...)
}

func (tp *tap) Addr() string { return tp.ln.Addr().String() }

// stack is a full bearer -> ranger -> provider path with a tap on the leg the
// relay handles.
type stack struct {
	bearerAddr string
	tap        *tap
}

func newStack(t *testing.T, handler http.HandlerFunc) *stack {
	t.Helper()

	// --- provider ---------------------------------------------------------
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
	provider := &http.Server{Handler: handler}
	go func() { _ = provider.Serve(providerLn) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	})

	pool := x509.NewCertPool()
	pool.AddCert(providerCert.Leaf)

	// --- tap, standing in for tcpdump on the relay host -------------------
	tp := newTap(t, providerLn.Addr().String())

	// --- ranger -----------------------------------------------------------
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

	// --- bearer -----------------------------------------------------------
	dialer, err := tunnel.New(tunnel.Config{
		Relay:  relay.Addr().String(),
		Pin:    relayPin,
		Secret: secret,
	})
	if err != nil {
		t.Fatalf("tunnel.New: %v", err)
	}
	client, err := bearer.New(bearer.Config{
		Addr:            "127.0.0.1:0",
		Upstream:        "https://" + tp.Addr(),
		Dialer:          dialer,
		UpstreamRootCAs: pool,
	})
	if err != nil {
		t.Fatalf("bearer.New: %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("bearer listen: %v", err)
	}
	go func() { _ = client.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Shutdown(ctx)
	})

	return &stack{bearerAddr: client.Addr().String(), tap: tp}
}

// TestRelayCannotReadWhatItCarries is the load-bearing test. Everything
// crossing the relay is captured and searched for the prompt and the key.
func TestRelayCannotReadWhatItCarries(t *testing.T) {
	s := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), promptMarker) {
			t.Errorf("provider did not receive the prompt; got %q", body)
		}
		if got := r.Header.Get("X-Api-Key"); got != apiKeyMarker {
			t.Errorf("provider received X-Api-Key %q, want the caller's key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%q}]}`, replyMarker)
	})

	req, err := http.NewRequest("POST", "http://"+s.bearerAddr+"/v1/messages",
		strings.NewReader(fmt.Sprintf(`{"model":"test","messages":[{"role":"user","content":%q}]}`, promptMarker)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Api-Key", apiKeyMarker)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %q", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), replyMarker) {
		t.Fatalf("client did not receive the model's reply; got %q", body)
	}

	// The request completed, so the relay definitely carried these bytes.
	captured := s.tap.Bytes()
	if len(captured) == 0 {
		t.Fatal("captured nothing; the tap is not on the path and this test proves nothing")
	}

	// Control. Absence of a marker is only meaningful if the tap is really on
	// the wire and really seeing the encrypted stream. A capture that began
	// with "POST /v1/messages" would mean the traffic was plaintext and the
	// markers were missing for some other reason. TLS records start with a
	// content type byte (0x16, handshake) and a major version of 0x03.
	if len(captured) < 3 || captured[0] != 0x16 || captured[1] != 0x03 {
		t.Fatalf("capture does not begin with a TLS handshake record (got % x); "+
			"the tap is not seeing the encrypted leg, so the absence of markers below proves nothing",
			captured[:min(8, len(captured))])
	}

	for _, marker := range []struct{ name, value string }{
		{"the prompt", promptMarker},
		{"the API key", apiKeyMarker},
		{"the model's reply", replyMarker},
	} {
		if bytes.Contains(captured, []byte(marker.value)) {
			t.Errorf("%s appeared in plaintext in the %d bytes the relay handled; "+
				"the relay is able to read what it carries and the central claim is false",
				marker.name, len(captured))
		}
	}

	t.Logf("relay handled %d bytes; neither prompt, key nor reply was recoverable from them", len(captured))
}

// TestStreamingSurvivesTheFullPath checks that server-sent events still arrive
// incrementally with a relay in the middle. Buffering anywhere on the path
// would turn token streaming into one long pause.
func TestStreamingSurvivesTheFullPath(t *testing.T) {
	release := make(chan struct{})
	s := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("provider ResponseWriter is not a Flusher")
			return
		}
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"text\":\"first\"}\n\n")
		fl.Flush()
		<-release
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"text\":\"second\"}\n\n")
		fl.Flush()
	})

	resp, err := http.Get("http://" + s.bearerAddr + "/v1/messages")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	first := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		first <- string(buf[:n])
	}()

	select {
	case chunk := <-first:
		if !strings.Contains(chunk, "first") {
			t.Fatalf("first chunk = %q", chunk)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("no data arrived before the second chunk was written; something on the path is buffering the stream")
	}

	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "second") {
		t.Errorf("second chunk missing; got %q", rest)
	}
}

// TestProviderNeverSeesForwardingHeaders checks the other half of the promise:
// the provider learns what was asked but not where it came from.
func TestProviderNeverSeesForwardingHeaders(t *testing.T) {
	seen := make(chan http.Header, 1)
	s := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		fmt.Fprint(w, "{}")
	})

	req, _ := http.NewRequest("POST", "http://"+s.bearerAddr+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	h := <-seen
	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Host", "Forwarded", "Proxy-Authorization"} {
		if v := h.Get(name); v != "" {
			t.Errorf("provider saw %s: %q; it must not learn where the request came from", name, v)
		}
	}
}

// TestLargeBodyRoundTrips guards the byte pump against truncation and
// interleaving under a payload bigger than any single buffer on the path.
func TestLargeBodyRoundTrips(t *testing.T) {
	const size = 1 << 20 // 1 MiB

	s := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("provider read: %v", err)
			return
		}
		if len(body) != size {
			t.Errorf("provider received %d bytes, want %d", len(body), size)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})

	payload := bytes.Repeat([]byte("osanwe-"), size/7+1)[:size]
	resp, err := http.Post("http://"+s.bearerAddr+"/v1/messages", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip corrupted the body: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestBearerFailsClosedWhenRelayIsDown checks that losing the relay produces a
// readable error rather than a request that silently bypasses it.
func TestBearerFailsClosedWhenRelayIsDown(t *testing.T) {
	// A relay address that nothing is listening on.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()

	_, pin, err := certs.SelfSigned([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	dialer, err := tunnel.New(tunnel.Config{Relay: deadAddr, Pin: pin, Secret: secret, Timeout: time.Second})
	if err != nil {
		t.Fatalf("tunnel.New: %v", err)
	}
	client, err := bearer.New(bearer.Config{Addr: "127.0.0.1:0", Dialer: dialer})
	if err != nil {
		t.Fatalf("bearer.New: %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = client.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Shutdown(ctx)
	})

	resp, err := http.Get("http://" + client.Addr().String() + "/v1/messages")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502; a request must never quietly bypass an unavailable relay", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "osanwe_tunnel_error") {
		t.Errorf("body = %q, want a parseable JSON error", body)
	}
}
