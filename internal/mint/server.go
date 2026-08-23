package mint

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// MaxIssueBody bounds an issuance request. A blinded message is one modulus
// wide; anything substantially larger is not a client this mint can help.
const MaxIssueBody = 16 << 10

// IssueRequest is the body of POST /issue.
type IssueRequest struct {
	// Receipt is whatever the payment rail issued. The mint passes it to the
	// Authorizer and never interprets it.
	Receipt string `json:"receipt,omitempty"`

	// Blinded is the blinded message, base64url without padding.
	Blinded string `json:"blinded"`
}

// IssueResponse is the body of a successful POST /issue.
type IssueResponse struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// Server exposes a Mint over HTTP.
//
// There are two endpoints and no others, which is deliberate. Every additional
// thing a mint offers is another opportunity to correlate: an endpoint that
// reported a buyer's remaining balance, for instance, would have to know who
// was asking, and would then hold exactly the link the blinding removes.
type Server struct {
	m   *Mint
	log *slog.Logger
}

// NewServer wraps a Mint.
func NewServer(m *Mint, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{m: m, log: log}
}

// Handler returns the mint's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /key", s.handleKey)
	mux.HandleFunc("POST /issue", s.handleIssue)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

// handleKey publishes the verification key.
//
// It is served openly because it has to be: a token is only worth anything
// because a gateway with no relationship to this mint can check it. Clients
// must still confirm the key id they got out of band matches, since a mint
// that handed every buyer a different key would put each of them in an
// anonymity set of one while appearing to behave correctly.
func (s *Server) handleKey(w http.ResponseWriter, r *http.Request) {
	pub := s.m.PublicKey()
	der, err := marshalPublicPEM(pub)
	if err != nil {
		http.Error(w, "could not encode the key", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("X-Osanwe-Mint-Key-Id", s.m.KeyID())
	w.Write(der)
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxIssueBody+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the request")
		return
	}
	if len(body) > MaxIssueBody {
		writeErr(w, http.StatusRequestEntityTooLarge, "issuance request is too large")
		return
	}

	var req IssueRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "could not parse the request")
		return
	}
	blinded, err := base64.RawURLEncoding.DecodeString(req.Blinded)
	if err != nil || len(blinded) == 0 {
		writeErr(w, http.StatusBadRequest, "blinded message is not valid base64url")
		return
	}

	sig, err := s.m.Issue(r.Context(), []byte(req.Receipt), blinded)
	if err != nil {
		// Payment failures and malformed input are told apart for the caller,
		// because the fixes are entirely different. Neither response mentions
		// the blinded value, which is the one thing here worth not echoing.
		switch {
		case errorIs(err, ErrNotPaid):
			writeErr(w, http.StatusPaymentRequired, "no paid entitlement for this request")
		case errorIs(err, ErrDegenerate):
			writeErr(w, http.StatusBadRequest, "that blinded message is not usable")
		default:
			s.log.Error("issuance failed", "error", err)
			writeErr(w, http.StatusInternalServerError, "could not issue")
		}
		return
	}

	// Nothing about this issuance is logged. A mint that recorded even a
	// timestamp per request would hand anyone who took the logs a way to
	// correlate issuance against redemption by timing, which is the link the
	// blinding exists to break.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(IssueResponse{
		KeyID:     s.m.KeyID(),
		Signature: base64.RawURLEncoding.EncodeToString(sig),
	})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
