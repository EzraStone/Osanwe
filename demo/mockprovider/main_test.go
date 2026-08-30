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
