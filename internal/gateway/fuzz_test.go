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
