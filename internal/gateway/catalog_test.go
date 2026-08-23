package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestCatalogCarriesFactualPrivacyAndLimits(t *testing.T) {
	h := newRoutedHarness(t)
	resp, err := h.client.Get(h.url + "/v1/models")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var catalog modelCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if catalog.Object != "list" || catalog.SchemaVersion != 2 {
		t.Fatalf("catalog header = object %q schema %d", catalog.Object, catalog.SchemaVersion)
	}
	if len(catalog.Data) != 2 {
		t.Fatalf("catalog has %d models, want 2", len(catalog.Data))
	}
	for _, model := range catalog.Data {
		if model.ID == "" || model.Type != "model" {
			t.Errorf("identity = %+v", model)
		}
		if !model.Capabilities.Text || !model.Capabilities.Streaming || model.Capabilities.Tools || model.Capabilities.Images {
			t.Errorf("capabilities for %s = %+v", model.ID, model.Capabilities)
		}
		if model.Limits.MaxRequestBytes != DefaultMaxRequestBody || model.Limits.MaxOutputTokens != DefaultMaxOutputTokens {
			t.Errorf("limits for %s = %+v", model.ID, model.Limits)
		}
		if model.Osanwe.ProviderAccount != "pooled" || model.Osanwe.RelayContentAccess != "ciphertext_only" {
			t.Errorf("network privacy for %s = %+v", model.ID, model.Osanwe)
		}
		if model.Osanwe.GatewayContentAccess != "plaintext_until_attested_execution" {
			t.Errorf("gateway access for %s = %q", model.ID, model.Osanwe.GatewayContentAccess)
		}
		if model.Osanwe.ProviderRetention != "unknown" || model.Osanwe.ProviderTraining != "unknown" {
			t.Errorf("provider policy for %s was guessed: %+v", model.ID, model.Osanwe)
		}
		if model.Osanwe.ProviderIdentity != "unknown" || model.Osanwe.PolicySource != "" || model.Osanwe.PolicyCheckedAt != "" {
			t.Errorf("provider identity/source for %s was guessed: %+v", model.ID, model.Osanwe)
		}
		if model.Lifecycle.Experimental || model.Lifecycle.ExpiresAt != "" {
			t.Errorf("lifecycle for %s was guessed: %+v", model.ID, model.Lifecycle)
		}
	}
}

func TestCatalogCarriesSourcedProviderPolicyAndLifecycle(t *testing.T) {
	h := newRoutedHarness(t)
	expires := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	route := h.gw.cfg.Routes.byModel["deepseek-chat"]
	route.Policy = ProviderPolicy{
		Retention: ProviderRetentionRetained,
		Training:  ProviderTrainingUnknown,
		Identity:  ProviderIdentityUndisclosed,
		SourceURL: "https://openrouter.ai/stealth/ox-alpha",
		CheckedAt: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC),
	}
	route.Lifecycle = ModelLifecycle{Experimental: true, ExpiresAt: expires}
	h.gw.cfg.Routes.byModel["deepseek-chat"] = route
	h.gw.now = func() time.Time { return expires.Add(-time.Second) }

	resp, err := h.client.Get(h.url + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var catalog modelCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	var got *modelDescriptor
	for i := range catalog.Data {
		if catalog.Data[i].ID == "deepseek-chat" {
			got = &catalog.Data[i]
		}
	}
	if got == nil {
		t.Fatal("annotated model is missing")
	}
	if got.Osanwe.ProviderRetention != "retained" || got.Osanwe.ProviderTraining != "unknown" ||
		got.Osanwe.ProviderIdentity != "undisclosed" || got.Osanwe.PolicySource != route.Policy.SourceURL ||
		got.Osanwe.PolicyCheckedAt != "2026-08-22" {
		t.Fatalf("policy = %+v", got.Osanwe)
	}
	if !got.Lifecycle.Experimental || got.Lifecycle.ExpiresAt != "2026-08-29T00:00:00Z" {
		t.Fatalf("lifecycle = %+v", got.Lifecycle)
	}
}
