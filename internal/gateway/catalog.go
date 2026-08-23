package gateway

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"
)

const modelCatalogSchemaVersion = 2

type modelCatalog struct {
	Object        string            `json:"object"`
	SchemaVersion int               `json:"schema_version"`
	Data          []modelDescriptor `json:"data"`
}

type modelDescriptor struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Capabilities modelCapabilities `json:"capabilities"`
	Limits       modelLimits       `json:"limits"`
	Osanwe       modelPrivacy      `json:"osanwe"`
	Lifecycle    modelLifecycle    `json:"lifecycle"`
}

type modelCapabilities struct {
	Text      bool `json:"text"`
	Streaming bool `json:"streaming"`
	Tools     bool `json:"tools"`
	Images    bool `json:"images"`
}

type modelLimits struct {
	MaxRequestBytes int64 `json:"max_request_bytes"`
	MaxOutputTokens int   `json:"max_output_tokens"`
}

// modelPrivacy contains facts about this gateway protocol, not claims about a
// provider's changing policy. Unknown is explicit so clients never translate
// an absent field into a favorable answer.
type modelPrivacy struct {
	ProviderAccount      string `json:"provider_account"`
	RelayContentAccess   string `json:"relay_content_access"`
	GatewayContentAccess string `json:"gateway_content_access"`
	ConversationHistory  string `json:"conversation_history"`
	AddressSeparation    string `json:"address_separation"`
	ProviderRetention    string `json:"provider_retention"`
	ProviderTraining     string `json:"provider_training"`
	ProviderIdentity     string `json:"provider_identity"`
	PolicySource         string `json:"policy_source,omitempty"`
	PolicyCheckedAt      string `json:"policy_checked_at,omitempty"`
}

type modelLifecycle struct {
	Experimental bool   `json:"experimental"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// handleModels lists what this gateway carries, in the shape a provider does.
func (s *Server) handleModels(w http.ResponseWriter) {
	now := s.now()
	models := s.modelNamesAt(now)
	out := modelCatalog{
		Object:        "list",
		SchemaVersion: modelCatalogSchemaVersion,
		Data:          make([]modelDescriptor, 0, len(models)),
	}
	for _, model := range models {
		policy := ProviderPolicy{}.normalized()
		lifecycle := ModelLifecycle{}
		if s.cfg.Routes != nil {
			route, _ := s.cfg.Routes.LookupActive(model, now)
			policy, lifecycle = route.Policy.normalized(), route.Lifecycle
		}
		wireLifecycle := modelLifecycle{Experimental: lifecycle.Experimental}
		if !lifecycle.ExpiresAt.IsZero() {
			wireLifecycle.ExpiresAt = lifecycle.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		checkedAt := ""
		if !policy.CheckedAt.IsZero() {
			checkedAt = policy.CheckedAt.UTC().Format("2006-01-02")
		}
		// The upstream address and its credential are deliberately absent: a
		// client has no use for either, and either would be worth stealing.
		// Which vendor serves a model is not a secret and could not be kept
		// one -- the names say so themselves.
		out.Data = append(out.Data, modelDescriptor{
			ID:   model,
			Type: "model",
			Capabilities: modelCapabilities{
				Text: true, Streaming: true, Tools: false, Images: false,
			},
			Limits: modelLimits{
				MaxRequestBytes: s.cfg.MaxRequestBody,
				MaxOutputTokens: s.cfg.MaxOutputTokens,
			},
			Osanwe: modelPrivacy{
				ProviderAccount:      "pooled",
				RelayContentAccess:   "ciphertext_only",
				GatewayContentAccess: "plaintext_until_attested_execution",
				ConversationHistory:  "not_stored_by_osanwe_services",
				AddressSeparation:    "requires_independent_relay",
				ProviderRetention:    string(policy.Retention),
				ProviderTraining:     string(policy.Training),
				ProviderIdentity:     string(policy.Identity),
				PolicySource:         policy.SourceURL,
				PolicyCheckedAt:      checkedAt,
			},
			Lifecycle: wireLifecycle,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	setPrivateResponseHeaders(w.Header())
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) modelNames() []string {
	return s.modelNamesAt(s.now())
}

func (s *Server) modelNamesAt(now time.Time) []string {
	if s.cfg.Routes != nil {
		return s.cfg.Routes.ActiveModels(now)
	}
	models := append([]string(nil), s.cfg.Models...)
	slices.Sort(models)
	return models
}
