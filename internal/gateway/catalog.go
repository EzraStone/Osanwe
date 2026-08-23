package gateway

import (
	"encoding/json"
	"net/http"
	"slices"
)

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
