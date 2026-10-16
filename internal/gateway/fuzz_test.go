package gateway

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EzraStone/osanwe/internal/mint"
)

// Request bodies come from anonymous clients, and prepareRequest is what
// decides, before any token is spent, whether the gateway will pay to forward
// one. Whatever the bytes, it must not panic; and anything it accepts must be
// within the size limit, carry only supported fields and no duplicate names,
// name an allowed model with max_tokens in range, and be stable: validating
// the forwarded body again must accept it and produce the same bytes.
func FuzzPrepareRequest(f *testing.F) {
	priv, err := mint.GenerateKey(mint.MinKeyBits)
	if err != nil {
		f.Fatal(err)
	}
	s, err := New(Config{
		Addr: "127.0.0.1:0", Upstream: "https://api.anthropic.com", Models: []string{"demo"},
		MintKeys: map[string]*rsa.PublicKey{mint.KeyID(&priv.PublicKey): &priv.PublicKey},
		Spent:    mint.NewSpentSet(), Budget: UnlimitedBudget{},
		Credential: Credential{Header: "x-api-key", Value: "sk-pooled"}, Logger: quiet(),
	})
	if err != nil {
		f.Fatal(err)
	}

	for _, seed := range []string{
		`{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
		`{"model":"demo","max_tokens":1,"system":"be brief","stream":true,"temperature":0.5,"top_p":1,"stop_sequences":["x"],"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`,
		`{"model":"demo","model":"other","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"demo","max_tokens":64,"messages":[{"role":"user","role":"system","content":"hi"}]}`,
		`{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"demo","max_tokens":64,"messages":[{"role":"user","content":[{"type":"image"}]}]}`,
		`{"model":"demo","max_tokens":99999999,"messages":[{"role":"user","content":"hi"}]}`,
		`[]`, `null`, ``,
	} {
		f.Add([]byte(seed))
	}

	prepare := func(data []byte) (chosen, []byte, *policyError) {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		pick, b, perr := s.prepareRequest(r)
		if perr != nil {
			return pick, nil, perr
		}
		out, err := io.ReadAll(b)
		if err != nil {
			panic(err)
		}
		return pick, out, nil
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		pick, out, perr := prepare(data)
		if perr != nil {
			return
		}
		if int64(len(out)) > s.cfg.MaxRequestBody {
			t.Fatalf("forwarding %d bytes, over the %d-byte limit\n%q", len(out), s.cfg.MaxRequestBody, data)
		}
		if err := rejectDuplicateJSONNames(out); err != nil {
			t.Fatalf("forwarded body has a duplicate or invalid name: %v\n%q", err, out)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(out, &fields); err != nil {
			t.Fatalf("forwarded body is not a JSON object: %v\n%q", err, out)
		}
		for name := range fields {
			switch name {
			case "model", "max_tokens", "messages", "system", "stream", "temperature", "top_p", "stop_sequences":
			default:
				t.Fatalf("forwarded unsupported field %q\n%q", name, out)
			}
		}
		var model string
		var maxTokens int
		if json.Unmarshal(fields["model"], &model) != nil || model != "demo" || model != pick.model {
			t.Fatalf("forwarded model %q (chosen %q)\n%q", model, pick.model, out)
		}
		if json.Unmarshal(fields["max_tokens"], &maxTokens) != nil || maxTokens < 1 ||
			maxTokens > s.cfg.MaxOutputTokens || maxTokens != pick.maxOutputTokens {
			t.Fatalf("forwarded max_tokens %d (chosen %d, limit %d)\n%q", maxTokens, pick.maxOutputTokens, s.cfg.MaxOutputTokens, out)
		}

		_, again, perr := prepare(out)
		if perr != nil {
			t.Fatalf("the forwarded body is refused when validated again: %s\n%q", perr.message, out)
		}
		if !bytes.Equal(again, out) {
			t.Fatalf("validation is not stable:\n first %q\nsecond %q", out, again)
		}
	})
}

// A provider's reply is outside the gateway's control. Whatever an
// OpenAI-compatible provider returns, translating it must not panic, and what
// reaches the client must be well-formed Messages output.
func FuzzTranslateOpenAIResponse(f *testing.F) {
	f.Add([]byte(`{"id":"c1","model":"m","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	f.Add([]byte(`{"choices":[]}`))
	f.Add([]byte(`{"choices":[{"message":{"content":null},"finish_reason":"content_filter"}]}`))
	f.Add([]byte(`[]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := fromOpenAI(data)
		if err != nil {
			return
		}
		var message struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
		}
		if err := json.Unmarshal(out, &message); err != nil {
			t.Fatalf("translation is not JSON: %v\n%q", err, out)
		}
		if message.Type != "message" || message.Role != "assistant" || len(message.Content) != 1 || message.Content[0].Type != "text" || message.StopReason == "" {
			t.Fatalf("translation is not a Messages response: %s", out)
		}
	})
}

func FuzzTranslateOpenAIStream(f *testing.F) {
	f.Add([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	f.Add([]byte("data: {\"choices\":[]}\n\n"))
	f.Add([]byte(": keep-alive\n\ndata:\n\ndata: not json\n\n"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := io.ReadAll(streamFromOpenAI(io.NopCloser(bytes.NewReader(data))))
		var types []string
		for _, line := range bytes.Split(out, []byte("\n")) {
			payload, ok := bytes.CutPrefix(line, []byte("data: "))
			if !ok {
				continue
			}
			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("emitted an event that is not JSON: %q", payload)
			}
			types = append(types, event.Type)
		}
		if len(types) < 2 || types[0] != "message_start" || types[1] != "content_block_start" {
			t.Fatalf("stream did not open properly: %q", types)
		}
		if err == nil && types[len(types)-1] != "message_stop" {
			t.Fatalf("a stream that ended cleanly did not end with message_stop: %q", types)
		}
	})
}
