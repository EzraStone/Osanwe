package gateway

import (
	"encoding/json"
	"net/http"
	"slices"
)

const modelCatalogSchemaVersion = 1

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
}

// handleModels lists what this gateway carries, in the shape a provider does.
func (s *Server) handleModels(w http.ResponseWriter) {
	models := s.modelNames()
	out := struct {
		Data []map[string]string `json:"data"`
	}{Data: make([]map[string]string, 0, len(models))}
	for _, model := range models {
		// The upstream address and its credential are deliberately absent: a
		// client has no use for either, and either would be worth stealing.
		// Which vendor serves a model is not a secret and could not be kept
		// one -- the names say so themselves.
		out.Data = append(out.Data, map[string]string{"id": model, "type": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) modelNames() []string {
	if s.cfg.Routes != nil {
		return s.cfg.Routes.Models()
	}
	models := append([]string(nil), s.cfg.Models...)
	slices.Sort(models)
	return models
}
