package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsPublishesDemoCatalog(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	recorder := httptest.NewRecorder()

	handle(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var response struct {
		Object string `json:"object"`
		Data   []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Object != "list" || len(response.Data) != 1 || response.Data[0].ID != "demo" || response.Data[0].Object != "model" {
		t.Fatalf("catalog = %#v, want one demo model", response)
	}
}

func TestMessageReplyDisclosesLocalDemo(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hello"}]}`))
	recorder := httptest.NewRecorder()

	handle(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "Local demo only") || !strings.Contains(recorder.Body.String(), "no external model was called") {
		t.Fatalf("response did not disclose the canned local provider: %s", recorder.Body.String())
	}
}

// The interface always streams, so the streamed reply is the one people see.
// It has to carry the same disclosure, in an event sequence a client can parse.
func TestStreamedReplyDisclosesLocalDemo(t *testing.T) {
	saved := wordDelay
	wordDelay = 0
	t.Cleanup(func() { wordDelay = saved })

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"demo","stream":true,"messages":[{"role":"user","content":"hello there"}]}`))
	recorder := httptest.NewRecorder()
	handle(recorder, req)

	if ct := recorder.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var types []string
	var text strings.Builder
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("event is not JSON: %q", data)
		}
		types = append(types, ev.Type)
		text.WriteString(ev.Delta.Text)
	}
	if len(types) < 3 || types[0] != "message_start" || types[len(types)-1] != "message_stop" {
		t.Fatalf("event sequence = %q", types)
	}
	got := text.String()
	if !strings.HasPrefix(got, "Local demo only — no external model was called.") || !strings.Contains(got, "hello there") {
		t.Fatalf("streamed reply = %q, want the disclosure first and the prompt echoed", got)
	}
}

func TestUnknownPathsAreNotFound(t *testing.T) {
	recorder := httptest.NewRecorder()
	handle(recorder, httptest.NewRequest(http.MethodPost, "/v1/complete", strings.NewReader("{}")))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 so a client using the wrong API fails loudly", recorder.Code)
	}
}
