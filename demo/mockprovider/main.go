// Command mockprovider stands in for a model provider so the whole network can
// be demonstrated without an API key or an account.
//
// It speaks enough of the Messages API shape to be convincing: it accepts
// POST /v1/messages, echoes back what it was asked, and streams
// server-sent events when the request sets "stream": true.
//
// It also does something a real provider cannot do for us. With -tap it records
// the raw bytes arriving on its TCP socket, before TLS is terminated. Those
// bytes are exactly what the relay forwarded, so grepping that file is a direct
// test of whether the relay could have read the conversation.
//
// This is demo scaffolding. It is not part of the network and nothing else
// imports it.
package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/EzraStone/osanwe/internal/certs"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on")
	certOut := flag.String("cert-out", "", "write the generated certificate here so a client can trust it")
	addrOut := flag.String("addr-out", "", "write the listening address here")
	tapPath := flag.String("tap", "", "record raw bytes received before TLS termination")
	flag.Parse()

	cert, _, err := certs.SelfSigned([]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		log.Fatalf("mockprovider: %v", err)
	}

	if *certOut != "" {
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
		if err := os.WriteFile(*certOut, pemBytes, 0o644); err != nil {
			log.Fatalf("mockprovider: writing certificate: %v", err)
		}
	}

	raw, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("mockprovider: %v", err)
	}
	if *addrOut != "" {
		if err := os.WriteFile(*addrOut, []byte(raw.Addr().String()), 0o644); err != nil {
			log.Fatalf("mockprovider: writing address: %v", err)
		}
	}

	var listener net.Listener = raw
	if *tapPath != "" {
		f, err := os.Create(*tapPath)
		if err != nil {
			log.Fatalf("mockprovider: creating tap: %v", err)
		}
		defer f.Close()
		listener = &tappedListener{Listener: raw, tap: f}
	}

	srv := &http.Server{
		Handler:           http.HandlerFunc(handle),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}},
	}
	fmt.Fprintf(os.Stderr, "mockprovider listening on %s\n", raw.Addr())
	log.Fatal(srv.ServeTLS(listener, "", ""))
}

// handle answers a Messages-API-shaped request.
func handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]string{{
				"id":     "demo",
				"object": "model",
			}},
		})
		return
	}
	if r.URL.Path != "/v1/messages" {
		http.Error(w, `{"type":"error","error":{"message":"not found"}}`, http.StatusNotFound)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)

	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[len(req.Messages)-1].Content
	}

	// Report what the provider can see about the caller. This is the
	// interesting part of the demo: it sees the prompt and the API key, and
	// the relay's address rather than the user's.
	seen := map[string]string{
		"remote_addr":   r.RemoteAddr,
		"x_api_key":     r.Header.Get("X-Api-Key"),
		"user_agent":    r.Header.Get("User-Agent"),
		"forwarded_for": r.Header.Get("X-Forwarded-For"),
	}
	enc, _ := json.Marshal(seen)
	fmt.Fprintf(os.Stderr, "provider saw: %s\n", enc)

	// No quotes in the reply text: the demo pulls these deltas out of the SSE
	// stream with sed, and escaped quotes inside JSON would mangle it.
	reply := fmt.Sprintf("I read %d bytes and your last message was %s", len(body), prompt)

	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "msg_demo",
			"type":    "message",
			"role":    "assistant",
			"model":   req.Model,
			"content": []map[string]string{{"type": "text", "text": reply}},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	// Emit one word at a time with a pause, so a client that buffers instead
	// of streaming is immediately obvious to the eye.
	fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
	flusher.Flush()

	for _, word := range splitWords(reply) {
		chunk, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"delta": map[string]string{"type": "text_delta", "text": word + " "},
		})
		fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", chunk)
		flusher.Flush()
		time.Sleep(120 * time.Millisecond)
	}

	fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	flusher.Flush()
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// tappedListener records every byte read from an accepted connection, before
// TLS gets a chance to decrypt it.
type tappedListener struct {
	net.Listener
	mu  sync.Mutex
	tap *os.File
}

func (l *tappedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &tappedConn{Conn: c, parent: l}, nil
}

type tappedConn struct {
	net.Conn
	parent *tappedListener
}

func (c *tappedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.parent.mu.Lock()
		_, _ = c.parent.tap.Write(p[:n])
		c.parent.mu.Unlock()
	}
	return n, err
}
