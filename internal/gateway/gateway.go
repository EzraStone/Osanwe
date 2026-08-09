// Package gateway implements mithlond, the far side of the network.
//
// A relay hides who is asking. This hides what they are asking with: the
// client presents a blind-signed token instead of an account, and the gateway
// calls the provider using a credential the client never holds and never sees.
// The provider bills the gateway's account and learns nothing about which of
// its users produced any particular request.
//
// # What this component knows, and what that costs
//
// The gateway reads prompts. That is not an oversight, it is the deal: someone
// has to attach a real credential and speak to the provider, and TLS from the
// client cannot both terminate here and stay end-to-end. So the split is:
//
//	ranger    sees your address, never your words
//	mithlond  sees your words, never your address
//
// Neither sits on both sides, and the security of the whole arrangement is that
// they do not collude. This is the same assumption Apple's Private Relay makes,
// and it is stated in the package that depends on it rather than in a document
// nobody reads.
//
// # What is missing
//
// The design calls for this to run inside an attested TEE, so that "the
// gateway sees prompts" means the hardware sees them while the operator
// provably cannot, and a client can check the exact binary before sending
// anything. None of that is here. Until it is, running a gateway means asking
// users to trust its operator, and that should be said plainly to them rather
// than implied away by the word "anonymous".
package gateway

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

// TokenHeader carries the token. It is deliberately not Authorization, which
// belongs to the provider credential this gateway substitutes.
const TokenHeader = "X-Osanwe-Token"

// TokenOutcomeHeader tells a token-paying client what happened to the token it
// presented. The gateway always removes a provider-supplied value and writes
// this itself, so a provider cannot pretend that a spent token was refunded.
//
// A client may trust this only from the gateway it authenticated (bearer does
// so over TLS with system or explicitly configured roots), never from an
// arbitrary provider.
const TokenOutcomeHeader = "X-Osanwe-Token-Outcome"

const (
	TokenOutcomeSpent    = "spent"
	TokenOutcomeRefunded = "refunded"
	TokenOutcomeRejected = "rejected"
	TokenOutcomeInvalid  = "invalid"

	canonicalAccept           = "application/json"
	canonicalContentType      = "application/json"
	canonicalAnthropicVersion = "2023-06-01"
)

// Timeouts. As in bearer, no overall response timeout is set: a model thinking
// for a long time and then streaming for longer is ordinary traffic here.
const (
	DefaultResponseHeaderTimeout = 5 * time.Minute
	DefaultReadHeaderTimeout     = 30 * time.Second
	DefaultMaxRequestBody        = 1 << 20
	MaximumMaxRequestBody        = 16 << 20
	DefaultMaxOutputTokens       = 4096
	MaximumMaxOutputTokens       = 65536
)

// Credential is how the gateway authenticates to the provider.
type Credential struct {
	// Header is the provider's credential header: "x-api-key" for Anthropic,
	// "authorization" for anything OpenAI-compatible.
	Header string

	// Prefix goes before the value, "Bearer " where the provider expects it.
	Prefix string

	// Value is the pooled key.
	Value string
}

func (c Credential) valid() error {
	header := strings.TrimSpace(c.Header)
	switch {
	case strings.EqualFold(header, "authorization"),
		strings.EqualFold(header, "x-api-key"),
		strings.EqualFold(header, "api-key"):
		// These are the credential shapes the gateway deliberately supports.
	default:
		return errors.New("gateway: Credential.Header must be Authorization, X-Api-Key, or Api-Key")
	}
	if c.Value == "" || strings.TrimSpace(c.Value) != c.Value {
		return errors.New("gateway: Credential.Value is required; a gateway with no provider credential cannot answer anything")
	}
	if !safeHeaderFragment(c.Prefix) || !safeHeaderFragment(c.Value) {
		return errors.New("gateway: credential prefix and value must not contain control characters")
	}
	return nil
}

func safeHeaderFragment(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

// Config configures a Server.
type Config struct {
	// Addr to listen on.
	Addr string

	// Upstream is the provider base URL.
	Upstream string

	// MintKeys are the mint public keys whose tokens this gateway accepts,
	// by key id. Several are held at once so a key rotation has an overlap
	// window instead of invalidating everything in flight.
	MintKeys map[string]*rsa.PublicKey

	// Spent records redeemed tokens. Required: without it every token is
	// infinitely reusable.
	Spent mint.RedemptionStore

	// Budget atomically bounds aggregate provider work. Required: per-request
	// limits alone still allow an unbounded number of individually valid calls.
	Budget Budget

	// Credential authenticates the gateway to the provider, when there is a
	// single one. Ignored if Routes is set.
	Credential Credential

	// Routes, when set, chooses the provider per request from the model asked
	// for. Upstream and Credential are then unused, and a model with no route
	// is refused rather than sent somewhere surprising.
	Routes *Routes

	// Models is the exact allowlist a single-upstream gateway will pay for.
	// It is required without Routes. A pooled credential paired with an open
	// model selector lets one cheap token buy the provider's most expensive
	// model, which is not a defensible payment boundary.
	Models []string

	// MaxRequestBody and MaxOutputTokens bound what one token can buy. Zero
	// selects conservative defaults. These are per-request guardrails, not a
	// replacement for an account budget and aggregate rate/cost limiting.
	MaxRequestBody  int64
	MaxOutputTokens int

	// UpstreamRootCAs overrides the roots used to verify the provider.
	//
	// It exists for a self-hosted or enterprise provider endpoint presenting a
	// privately-issued certificate, which would otherwise be unreachable. As
	// in bearer, there is deliberately no option to skip verification: the
	// pooled credential is attached to this connection, and a gateway that
	// could be talked into trusting anything would hand it to whoever asked.
	UpstreamRootCAs *x509.CertPool

	ResponseHeaderTimeout time.Duration
	Logger                *slog.Logger
}

// Metrics are cumulative counters. There is deliberately no per-request
// record: a gateway that kept one would hold precisely the log this design
// exists to prevent anyone from having.
type Metrics struct {
	Accepted    atomic.Int64
	NoToken     atomic.Int64
	BadToken    atomic.Int64
	Replayed    atomic.Int64
	UpstreamErr atomic.Int64
	NoRoute     atomic.Int64
	Rejected    atomic.Int64
	Refunded    atomic.Int64
	BudgetFull  atomic.Int64
	BudgetErr   atomic.Int64
}

// Server is the gateway.
type Server struct {
	cfg      Config
	log      *slog.Logger
	upstream *url.URL
	models   map[string]struct{}
	metrics  Metrics

	http     *http.Server
	proxy    *httputil.ReverseProxy
	listener net.Listener
}

// routeKey carries the chosen provider from the handler to the rewrite hook,
// which cannot look it up itself: the body has already been consumed by then.
type routeKey struct{}

// tokenKey carries the redeemed token to the error hook, so the one failure
// that proves the provider did no work can be refunded.
type tokenKey struct{}

// budgetKey carries the aggregate reservation to the transport error hook.
type budgetKey struct{}

// chosen is what the handler worked out and the hooks need.
type chosen struct {
	route Route
	model string

	maxOutputTokens int

	// path is where the request should go, which differs from where it
	// arrived when the provider speaks another API.
	path string

	// translated says the answer has to be converted back on the way out.
	translated bool
}

// New validates a Config and returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("gateway: Addr is required")
	}
	if cfg.Upstream == "" && cfg.Routes == nil {
		return nil, errors.New("gateway: Upstream is required, unless Routes chooses one per request")
	}
	if len(cfg.MintKeys) == 0 {
		return nil, errors.New("gateway: at least one mint key is required; a gateway with none would accept no token, or worse, any")
	}
	if cfg.Spent == nil {
		return nil, errors.New("gateway: a RedemptionStore is required; without one every token can be spent forever")
	}
	if cfg.Budget == nil {
		return nil, errors.New("gateway: a Budget is required; without one aggregate provider spend is unbounded")
	}
	if cfg.Routes == nil {
		if err := cfg.Credential.valid(); err != nil {
			return nil, err
		}
		cfg.Credential.Header = http.CanonicalHeaderKey(strings.TrimSpace(cfg.Credential.Header))
		if len(cfg.Models) == 0 {
			return nil, errors.New("gateway: Models is required without Routes; a pooled credential must not pay for arbitrary models")
		}
	} else if len(cfg.Models) != 0 {
		return nil, errors.New("gateway: Models and Routes cannot both be set; route model names are already the allowlist")
	}
	if cfg.MaxRequestBody == 0 {
		cfg.MaxRequestBody = DefaultMaxRequestBody
	}
	if cfg.MaxRequestBody < 1 || cfg.MaxRequestBody > MaximumMaxRequestBody {
		return nil, fmt.Errorf("gateway: MaxRequestBody must be between 1 and %d bytes", MaximumMaxRequestBody)
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if cfg.MaxOutputTokens < 1 || cfg.MaxOutputTokens > MaximumMaxOutputTokens {
		return nil, fmt.Errorf("gateway: MaxOutputTokens must be between 1 and %d", MaximumMaxOutputTokens)
	}
	if cfg.ResponseHeaderTimeout <= 0 {
		cfg.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// With a route table the upstream is chosen per request, so a blank one is
	// correct rather than missing.
	up := &url.URL{}
	if cfg.Upstream != "" {
		parsed, err := url.Parse(cfg.Upstream)
		if err != nil {
			return nil, fmt.Errorf("gateway: parsing upstream %q: %w", cfg.Upstream, err)
		}
		if parsed.Scheme != "https" {
			return nil, fmt.Errorf("gateway: upstream must be https, got %q; the pooled credential would otherwise cross the network in the clear", cfg.Upstream)
		}
		if parsed.Host == "" {
			return nil, fmt.Errorf("gateway: upstream %q has no host", cfg.Upstream)
		}
		if parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("gateway: upstream %q must not contain user information, a query, or a fragment", cfg.Upstream)
		}
		up = parsed
	}

	// Every key must be usable, checked now rather than at the first request.
	for id, k := range cfg.MintKeys {
		if k == nil {
			return nil, fmt.Errorf("gateway: mint key %q is nil", id)
		}
		if got := mint.KeyID(k); got != id {
			return nil, fmt.Errorf("gateway: mint key filed under %q is actually %s", id, got)
		}
	}

	models := make(map[string]struct{})
	if cfg.Routes != nil {
		for _, model := range cfg.Routes.Models() {
			models[model] = struct{}{}
		}
	} else {
		for _, model := range cfg.Models {
			if model == "" || strings.TrimSpace(model) != model {
				return nil, fmt.Errorf("gateway: model allowlist contains an empty or whitespace-padded name %q", model)
			}
			if _, duplicate := models[model]; duplicate {
				return nil, fmt.Errorf("gateway: model %q appears twice in the allowlist", model)
			}
			models[model] = struct{}{}
		}
	}

	s := &Server{cfg: cfg, log: cfg.Logger, upstream: up, models: models}
	s.proxy = &httputil.ReverseProxy{
		Rewrite:   s.rewrite,
		Transport: s.transport(),
		// -1 flushes each write immediately, which is what keeps server-sent
		// events arriving token by token instead of in one lump at the end.
		FlushInterval:  -1,
		ModifyResponse: s.modifyResponse,
		ErrorHandler:   s.handleError,
	}
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           http.HandlerFunc(s.ServeHTTP),
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
	}
	return s, nil
}

func (s *Server) transport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    s.cfg.UpstreamRootCAs,
		},
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: s.cfg.ResponseHeaderTimeout,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
	}
}

// ServeHTTP verifies payment, proves that the request is within the narrow
// inference policy, reserves the token, and only then proxies it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The catalog is free and needs no token. Charging to find out what is on
	// offer, or making a client spend one to discover a typo, would be an odd
	// way to run a shop.
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		if r.URL.ForceQuery || r.URL.RawQuery != "" {
			s.metrics.Rejected.Add(1)
			s.refuseToken(w, http.StatusBadRequest, "query_not_allowed", TokenOutcomeRejected,
				"The local model catalog does not accept a query string.")
			return
		}
		s.handleModels(w)
		return
	}

	s.metrics.Accepted.Add(1)

	raw := r.Header.Get(TokenHeader)
	if raw == "" {
		s.metrics.NoToken.Add(1)
		s.refuseToken(w, http.StatusUnauthorized, "no_token",
			TokenOutcomeInvalid,
			"This gateway takes a token, not an API key. Present one in "+TokenHeader+".")
		return
	}

	tok, err := mint.ParseToken(raw)
	if err != nil {
		s.metrics.BadToken.Add(1)
		// The parse error is not echoed. It describes bytes the client sent,
		// so it tells them nothing they do not know, and reflecting attacker
		// input into a response is a habit worth not having.
		s.refuseToken(w, http.StatusUnauthorized, "bad_token", TokenOutcomeInvalid,
			"That token could not be read.")
		return
	}

	key, ok := s.cfg.MintKeys[tok.KeyID]
	if !ok {
		s.metrics.BadToken.Add(1)
		s.refuseToken(w, http.StatusUnauthorized, "unknown_mint", TokenOutcomeInvalid,
			"That token was signed by a mint this gateway does not accept, or by a key that has been retired.")
		return
	}
	if err := mint.Verify(key, tok); err != nil {
		s.metrics.BadToken.Add(1)
		s.refuseToken(w, http.StatusUnauthorized, "bad_token", TokenOutcomeInvalid,
			"That token is not valid.")
		return
	}

	// Validate the paid surface before reserving the token. This gateway is an
	// inference endpoint, not a general authenticated proxy into the provider
	// account: only an exact Messages request can ever receive the pooled key.
	pick, requestBody, policyErr := s.prepareRequest(r)
	if policyErr != nil {
		s.metrics.Rejected.Add(1)
		if policyErr.noRoute {
			s.metrics.NoRoute.Add(1)
		}
		if policyErr.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", http.MethodPost)
		}
		s.refuseToken(w, policyErr.status, policyErr.kind,
			TokenOutcomeRejected, policyErr.message)
		return
	}

	// Reserve aggregate provider capacity before spending the token. A full or
	// unavailable budget is an operator-side refusal and must not take payment.
	budgetReservation, err := s.cfg.Budget.Reserve(r.Context(), BudgetRequest{
		Model: pick.model, InputBytes: requestBody.size, MaxOutputTokens: pick.maxOutputTokens,
	})
	if err != nil {
		var limitErr *BudgetLimitError
		if errors.As(err, &limitErr) {
			s.metrics.BudgetFull.Add(1)
			retry := time.Until(limitErr.Reset)
			if retry < time.Second {
				retry = time.Second
			}
			w.Header().Set("Retry-After", strconv.FormatInt(int64(retry.Round(time.Second)/time.Second), 10))
			s.refuseToken(w, http.StatusTooManyRequests, "gateway_budget_exhausted", TokenOutcomeRejected,
				"The gateway has reached its aggregate provider budget. No token was spent; retry after the advertised interval.")
			return
		}
		s.metrics.BudgetErr.Add(1)
		s.log.Error("aggregate budget reservation failed", "error", err)
		s.refuseToken(w, http.StatusServiceUnavailable, "gateway_budget_unavailable", TokenOutcomeRejected,
			"The gateway could not safely reserve provider capacity. No token was spent.")
		return
	}

	// Spend before forwarding, never after. Marking it afterwards would leave
	// a window in which the same token, sent concurrently, buys several
	// requests -- and that window is exactly what an attacker would aim for.
	if err := s.cfg.Spent.Spend(tok); err != nil {
		s.releaseBudget(budgetReservation, "token reservation failed")
		if errors.Is(err, mint.ErrAlreadySpent) {
			s.metrics.Replayed.Add(1)
			s.refuseToken(w, http.StatusConflict, "token_spent", TokenOutcomeInvalid,
				"That token has already been used.")
			return
		}
		s.log.Error("redemption store refused a token reservation", "error", err)
		s.refuseToken(w, http.StatusServiceUnavailable, "redemption_unavailable", TokenOutcomeInvalid,
			"The gateway could not safely reserve that token. It was not forwarded; do not retry it until the operator confirms the redemption store is healthy.")
		return
	}

	r.Body = requestBody
	r.ContentLength = int64(requestBody.size)
	r = r.WithContext(context.WithValue(r.Context(), tokenKey{}, tok))
	r = r.WithContext(context.WithValue(r.Context(), budgetKey{}, budgetReservation))
	if s.cfg.Routes != nil {
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, pick))
	}

	s.proxy.ServeHTTP(w, r)
}

// body is a replayable request body, so ContentLength stays honest.
type body struct {
	io.ReadCloser
	size int
}

type policyError struct {
	status  int
	kind    string
	message string
	noRoute bool
}

// prepareRequest is the credential boundary. A value returned from here is
// the only request rewrite is allowed to send to a provider.
func (s *Server) prepareRequest(r *http.Request) (chosen, *body, *policyError) {
	reject := func(status int, kind, message string) (chosen, *body, *policyError) {
		return chosen{}, nil, &policyError{status: status, kind: kind, message: message}
	}
	if r.Method != http.MethodPost {
		return reject(http.StatusMethodNotAllowed, "method_not_allowed",
			"This gateway pays only for POST /v1/messages inference requests.")
	}
	if r.URL.Path != "/v1/messages" {
		return reject(http.StatusNotFound, "unsupported_endpoint",
			"This gateway pays only for /v1/messages inference requests; provider account, file, batch, fine-tuning, and administrative endpoints are not exposed.")
	}
	if r.URL.ForceQuery || r.URL.RawQuery != "" {
		return reject(http.StatusBadRequest, "query_not_allowed",
			"Inference requests must not contain a query string.")
	}
	if r.Header.Get("Content-Encoding") != "" {
		return reject(http.StatusUnsupportedMediaType, "content_encoding_not_allowed",
			"Compressed request bodies are not accepted because their expanded cost cannot be bounded before reading them.")
	}
	if len(r.Trailer) != 0 || r.Header.Get("Trailer") != "" {
		return reject(http.StatusBadRequest, "trailers_not_allowed",
			"Inference requests must not contain HTTP trailers; no token was spent.")
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return reject(http.StatusUnsupportedMediaType, "content_type",
			"Inference requests must use Content-Type: application/json.")
	}
	if r.ContentLength > s.cfg.MaxRequestBody {
		return reject(http.StatusRequestEntityTooLarge, "request_too_large",
			fmt.Sprintf("The request is over this gateway's %d-byte limit.", s.cfg.MaxRequestBody))
	}

	defer r.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.MaxRequestBody+1))
	if err != nil {
		return reject(http.StatusBadRequest, "request_read", "The request body could not be read.")
	}
	if int64(len(buf)) > s.cfg.MaxRequestBody {
		return reject(http.StatusRequestEntityTooLarge, "request_too_large",
			fmt.Sprintf("The request is over this gateway's %d-byte limit.", s.cfg.MaxRequestBody))
	}
	if err := rejectDuplicateJSONNames(buf); err != nil {
		if errors.Is(err, errDuplicateJSONField) {
			return reject(http.StatusBadRequest, "duplicate_json_field",
				"The request contains a duplicate JSON field. No token was spent.")
		}
		return reject(http.StatusBadRequest, "bad_json", "The request body is not one valid JSON object.")
	}

	// Decode to raw fields and marshal it again before forwarding. The recursive
	// duplicate-name pass above removes parser ambiguity at every object level;
	// re-encoding then removes alternate whitespace and escape spellings too.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(buf, &fields); err != nil || fields == nil {
		return reject(http.StatusBadRequest, "bad_json", "The request body is not one valid JSON object.")
	}
	allowedFields := map[string]struct{}{
		"model": {}, "max_tokens": {}, "messages": {}, "system": {},
		"stream": {}, "temperature": {}, "top_p": {}, "stop_sequences": {},
	}
	var unsupported []string
	for name := range fields {
		if _, ok := allowedFields[name]; !ok {
			unsupported = append(unsupported, name)
		}
	}
	if len(unsupported) != 0 {
		slices.Sort(unsupported)
		name := unsupported[0]
		if len(name) > 80 {
			name = name[:80] + "..."
		}
		return reject(http.StatusUnprocessableEntity, "unsupported_field",
			fmt.Sprintf("The top-level request field %q is not supported by this gateway; no token was spent.", name))
	}
	badType := func(name, expectation string) (chosen, *body, *policyError) {
		return reject(http.StatusUnprocessableEntity, "invalid_field",
			fmt.Sprintf("The request field %q must be %s; no token was spent.", name, expectation))
	}
	isNull := func(raw json.RawMessage) bool {
		return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}

	// Validate the deliberately small Messages subset before redemption. Text
	// only is an economic boundary, not merely a first implementation: a short
	// remote-image URL can make a provider fetch and tokenize content far larger
	// than this gateway's JSON byte limit, and nested cache controls can change
	// billing. Rich content needs explicit source/type validation and pricing
	// before it can safely be added.
	messageBytes, ok := fields["messages"]
	if !ok {
		return badType("messages", "an array")
	}
	var messages []json.RawMessage
	if json.Unmarshal(messageBytes, &messages) != nil || isNull(messageBytes) || len(messages) == 0 {
		return badType("messages", "a non-empty array")
	}
	for i, raw := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil || message == nil {
			return badType("messages", fmt.Sprintf("an array of message objects (item %d is invalid)", i))
		}
		for name := range message {
			if name != "role" && name != "content" {
				return badType("messages", fmt.Sprintf("text-only role/content objects (item %d has unsupported field %q)", i, name))
			}
		}
		var role, content string
		roleRaw, hasRole := message["role"]
		contentRaw, hasContent := message["content"]
		if !hasRole || json.Unmarshal(roleRaw, &role) != nil || (role != "user" && role != "assistant") {
			return badType("messages", fmt.Sprintf("an array of message objects with user/assistant roles (item %d is invalid)", i))
		}
		if !hasContent || json.Unmarshal(contentRaw, &content) != nil || isNull(contentRaw) {
			return badType("messages", fmt.Sprintf("text content as a string (item %d is invalid)", i))
		}
	}
	if raw, ok := fields["system"]; ok {
		var text string
		if json.Unmarshal(raw, &text) != nil || isNull(raw) {
			return badType("system", "a text string")
		}
	}
	if raw, ok := fields["stream"]; ok {
		var value bool
		if json.Unmarshal(raw, &value) != nil || isNull(raw) {
			return badType("stream", "true or false")
		}
	}
	for _, name := range []string{"temperature", "top_p"} {
		if raw, ok := fields[name]; ok {
			var value float64
			if json.Unmarshal(raw, &value) != nil || isNull(raw) || value < 0 || value > 1 {
				return badType(name, "a number from 0 through 1")
			}
		}
	}
	if raw, ok := fields["stop_sequences"]; ok {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || isNull(raw) {
			return badType("stop_sequences", "an array of strings")
		}
		for _, valueRaw := range values {
			var value string
			if json.Unmarshal(valueRaw, &value) != nil || isNull(valueRaw) {
				return badType("stop_sequences", "an array of strings")
			}
		}
	}
	var model string
	if raw, ok := fields["model"]; !ok || json.Unmarshal(raw, &model) != nil || model == "" {
		return reject(http.StatusBadRequest, "no_model", "The request names no model.")
	}
	var maxTokens int
	if raw, ok := fields["max_tokens"]; !ok || json.Unmarshal(raw, &maxTokens) != nil || maxTokens < 1 {
		return reject(http.StatusBadRequest, "max_tokens_required",
			"The request must set max_tokens to a positive integer so one token has an explicit cost ceiling.")
	}
	if maxTokens > s.cfg.MaxOutputTokens {
		return reject(http.StatusUnprocessableEntity, "output_limit",
			fmt.Sprintf("The request asks for %d output tokens; this gateway permits at most %d per paid request.",
				maxTokens, s.cfg.MaxOutputTokens))
	}
	if _, ok := s.models[model]; !ok {
		models := s.modelNames()
		return chosen{}, nil, &policyError{
			status: http.StatusNotFound, kind: "no_route", noRoute: true,
			message: fmt.Sprintf("This gateway does not carry %q. It carries: %s",
				model, strings.Join(models, ", ")),
		}
	}
	buf, err = json.Marshal(fields)
	if err != nil {
		return reject(http.StatusBadRequest, "bad_json", "The request body could not be normalized safely.")
	}

	pick := chosen{path: "/v1/messages", model: model, maxOutputTokens: maxTokens}
	if s.cfg.Routes != nil {
		pick.route, _ = s.cfg.Routes.Lookup(model)
		// Clients speak the Messages API. A provider that speaks OpenAI's
		// receives the one supported translation, never a caller-selected path.
		if pick.route.Style == StyleOpenAI {
			path, converted, err := toOpenAI(buf)
			if err != nil {
				return reject(http.StatusUnprocessableEntity, "translation", err.Error())
			}
			pick.path, pick.translated = path, true
			buf = converted
		}
	}

	return pick, &body{ReadCloser: replay(buf), size: len(buf)}, nil
}

// rejectDuplicateJSONNames walks every object, including nested messages and
// content, before any policy decision is made. Go's ordinary map/struct
// decoding keeps the last duplicate name; another provider parser may keep the
// first. Rejecting duplicates prevents the two sides from enforcing different
// meanings while looking at the same signed-token request.
func rejectDuplicateJSONNames(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("gateway: JSON has trailing values")
		}
		return err
	}
	return nil
}

var errDuplicateJSONField = errors.New("gateway: duplicate JSON field")

func walkJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, composite := tok.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("gateway: JSON object name is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%w %q", errDuplicateJSONField, key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("gateway: malformed JSON object")
		}
	case '[':
		for dec.More() {
			if err := walkJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("gateway: malformed JSON array")
		}
	default:
		return errors.New("gateway: unexpected JSON delimiter")
	}
	return nil
}

// rewrite points the request at the provider and replaces the client's
// credentials with the gateway's own.
func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	upstream, credential := s.upstream, s.cfg.Credential
	path := pr.Out.URL.Path
	if pick, ok := pr.In.Context().Value(routeKey{}).(chosen); ok {
		if u, err := url.Parse(pick.route.Upstream); err == nil {
			upstream, credential = u, pick.route.credential()
		}
		path = pick.path
	}

	// Clear every caller-controlled alternate URL representation before the
	// credential is attached. net/url and transports may prefer Opaque or
	// RawPath over Path; leaving residue makes an exact path check illusory.
	pr.Out.URL.Scheme = upstream.Scheme
	pr.Out.URL.Opaque = ""
	pr.Out.URL.User = nil
	pr.Out.URL.Host = upstream.Host
	pr.Out.URL.Path = path
	pr.Out.URL.RawPath = ""
	pr.Out.URL.OmitHost = false
	pr.Out.URL.ForceQuery = false
	pr.Out.URL.RawQuery = ""
	pr.Out.URL.Fragment = ""
	pr.Out.URL.RawFragment = ""
	pr.Out.Host = upstream.Host

	if base := strings.TrimSuffix(upstream.Path, "/"); base != "" {
		pr.Out.URL.Path = base + pr.Out.URL.Path
	}

	// Start over from a small API-semantic allowlist. A blacklist inevitably
	// misses SDK versions, trace ids, runtime names, custom request ids, and
	// the next identifying header somebody invents. Credentials, cookies,
	// forwarding metadata, the Osanwe token, and its outcome all disappear by
	// construction.
	pr.Out.Header = allowedHeaders(pr.Out.Header,
		"Accept", "Content-Type", "Anthropic-Version")
	// Trailers and the parsed TransferEncoding field live outside Header. The
	// inbound body has already been read and rebuilt with a known length, so
	// neither is needed; retaining them would let a chunked caller smuggle
	// arbitrary identifying or credential-shaped trailer fields around the
	// positive header allowlist.
	pr.Out.Trailer = nil
	pr.Out.TransferEncoding = nil
	// These are gateway policy, not caller metadata. Keeping the three header
	// names while forwarding arbitrary values would leave a stable covert
	// identifier (for example a Content-Type parameter) on the anonymous path.
	pr.Out.Header.Set("Accept", canonicalAccept)
	pr.Out.Header.Set("Content-Type", canonicalContentType)
	if pick, routed := pr.In.Context().Value(routeKey{}).(chosen); !routed || pick.route.Style == StyleAnthropic {
		pr.Out.Header.Set("Anthropic-Version", canonicalAnthropicVersion)
	} else {
		pr.Out.Header.Del("Anthropic-Version")
	}
	// net/http otherwise manufactures Go-http-client/1.1 after Rewrite. An
	// explicitly empty value suppresses that default without forwarding the
	// caller's User-Agent.
	pr.Out.Header["User-Agent"] = []string{""}

	pr.Out.Header.Set(credential.Header, credential.Prefix+credential.Value)
}

func allowedHeaders(source http.Header, names ...string) http.Header {
	kept := make(http.Header, len(names)+1)
	for _, name := range names {
		if values, ok := source[http.CanonicalHeaderKey(name)]; ok {
			kept[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return kept
}

// modifyResponse records the outcome asserted to the client. Once forwarding
// begins the token is spent, including on 5xx: a provider may have processed
// and billed the request before returning an error, and HTTP gives the gateway
// no proof otherwise.
func (s *Server) modifyResponse(resp *http.Response) error {
	credential := s.cfg.Credential
	if pick, ok := resp.Request.Context().Value(routeKey{}).(chosen); ok && s.cfg.Routes != nil {
		credential = pick.route.credential()
	}
	stripProviderCredentialHeaders(resp.Header, credential)
	stripNetworkTriggerHeaders(resp.Header)
	// Trailer values arrive while the body is being streamed, after this hook
	// runs, so they cannot be inspected safely here. This API needs none: drop
	// the declaration and map rather than let response trailers bypass the
	// ordinary response-header policy.
	resp.Header.Del("Trailer")
	resp.Trailer = nil
	resp.TransferEncoding = nil

	// A provider must not get to decide whether the client's wallet reuses a
	// token. Delete any value it supplied before setting the gateway's result.
	resp.Header.Del(TokenOutcomeHeader)
	if redirectsAutomatically(resp.StatusCode) {
		status := resp.StatusCode
		replaceRedirectResponse(resp,
			"The provider attempted to redirect this request. Redirects are blocked because following one from the local client would bypass the anonymity relay.")
		resp.Header.Set(TokenOutcomeHeader, TokenOutcomeSpent)
		s.metrics.UpstreamErr.Add(1)
		s.log.Warn("provider redirect blocked after dispatch; token remains spent", "status", status)
		return nil
	}
	if pick, ok := resp.Request.Context().Value(routeKey{}).(chosen); ok && pick.translated {
		if err := translateBack(resp); err != nil {
			return err
		}
	}
	if resp.StatusCode < 500 {
		resp.Header.Set(TokenOutcomeHeader, TokenOutcomeSpent)
		return nil
	}
	s.metrics.UpstreamErr.Add(1)
	resp.Header.Set(TokenOutcomeHeader, TokenOutcomeSpent)
	// Status only. Logging the token would create a record that a particular
	// token was used at a particular moment, which is half of the link this
	// design removes.
	s.log.Warn("provider failed after dispatch; token remains spent", "status", resp.StatusCode)
	return nil
}

func redirectsAutomatically(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func replaceRedirectResponse(resp *http.Response, message string) {
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	body := []byte(fmt.Sprintf(`{"type":"error","error":{"type":"osanwe_redirect_blocked","message":%q}}`+"\n", message))
	resp.StatusCode = http.StatusBadGateway
	resp.Status = fmt.Sprintf("%d %s", http.StatusBadGateway, http.StatusText(http.StatusBadGateway))
	resp.Header = make(http.Header)
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.TransferEncoding = nil
	resp.Trailer = nil
}

func stripNetworkTriggerHeaders(header http.Header) {
	for _, name := range []string{
		"Location", "Refresh", "Link", "Alt-Svc", "Set-Cookie",
		"Report-To", "Reporting-Endpoints", "NEL", "Expect-CT",
		"Public-Key-Pins", "Public-Key-Pins-Report-Only",
		"Content-Security-Policy", "Content-Security-Policy-Report-Only",
	} {
		header.Del(name)
	}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			header.Del(name)
		}
	}
}

// stripProviderCredentialHeaders catches the accidental response-echo cases a
// gateway can prevent without buffering or rewriting model output. A provider
// necessarily knows its own credential and could encode it into an arbitrary
// response body, so this is defense against ordinary header reflection, not a
// claim that a malicious provider can be made to forget a secret it received.
func stripProviderCredentialHeaders(header http.Header, credential Credential) {
	for _, name := range []string{"Authorization", "X-Api-Key", "Api-Key"} {
		header.Del(name)
	}
	want := credential.Prefix + credential.Value
	for name, values := range header {
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed == credential.Value || trimmed == want {
				header.Del(name)
				break
			}
		}
	}
}

// translateBack converts a provider's answer into the shape the client asked
// in, leaving errors alone: a provider's own error message is more use to
// whoever reads it than a translation of one.
func translateBack(resp *http.Response) error {
	if resp.StatusCode >= 400 {
		return nil
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body = streamFromOpenAI(resp.Body)
		// Length is unknown once the bytes change shape, and a stale one would
		// truncate the answer.
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxRoutedBody))
	resp.Body.Close()
	if err != nil {
		return err
	}
	converted, err := fromOpenAI(raw)
	if err != nil {
		// Unrecognisable: hand back what arrived rather than nothing at all.
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
		resp.ContentLength = int64(len(raw))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(converted))
	resp.Header.Set("Content-Length", strconv.Itoa(len(converted)))
	resp.ContentLength = int64(len(converted))
	return nil
}

// handleError reports a transport failure, refunding only when the failure
// proves the request never reached the provider.
func (s *Server) handleError(w http.ResponseWriter, r *http.Request, err error) {
	s.metrics.UpstreamErr.Add(1)
	// The path is logged; the body never is, and neither is the token.
	s.log.Error("upstream request failed", "path", r.URL.Path, "error", err)

	if neverDispatched(err) {
		if reservation, ok := r.Context().Value(budgetKey{}).(BudgetReservation); ok {
			s.releaseBudget(reservation, "provider was never reached")
		}
		if tok, ok := r.Context().Value(tokenKey{}).(*mint.Token); ok {
			if refundErr := s.cfg.Spent.Refund(tok); refundErr != nil {
				// A refund that did not survive is not a refund. Say spent, so
				// the wallet does not resurrect a token the store may still
				// consider redeemed.
				s.log.Error("refund failed; the token stays spent", "error", refundErr)
				s.refuseToken(w, http.StatusBadGateway, "upstream_error", TokenOutcomeSpent,
					"The provider could not be reached, and the refund could not be recorded. Your token is not usable again.")
				return
			}
			s.metrics.Refunded.Add(1)
			// Status only. Logging the token would create a record that a
			// particular token was used at a particular moment, which is half
			// of the link this design removes.
			s.log.Warn("provider was never reached; token refunded")
			s.refuseToken(w, http.StatusBadGateway, "upstream_unreachable", TokenOutcomeRefunded,
				"The provider could not be reached. Your token has not been used.")
			return
		}
	}

	s.refuseToken(w, http.StatusBadGateway, "upstream_error", TokenOutcomeSpent,
		"The provider connection failed after dispatch. Your token remains spent because the gateway cannot prove the provider did no work.")
}

func (s *Server) releaseBudget(reservation BudgetReservation, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reservation.Release(ctx); err != nil {
		s.metrics.BudgetErr.Add(1)
		s.log.Error("aggregate budget release failed; capacity remains conservatively charged",
			"reason", reason, "error", err)
	}
}

// neverDispatched reports whether err proves no part of the request reached
// the provider.
//
// Only a failure to obtain a connection does. Once bytes are on the wire, an
// EOF, a reset, or a timeout is equally consistent with the provider having
// received, processed and billed the request before the failure -- refunding
// those would let a client harvest free inference by cutting the connection
// after the request was sent. A dial or DNS failure has no such reading: there
// was no connection to send anything on, so the provider did no work and the
// user should not pay for the operator's outage.
func neverDispatched(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	// "dial" covers connection refused, host unreachable and dial timeouts.
	// Read and write ops are deliberately excluded: by then the request may
	// already have been delivered.
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// refuse writes an error in the shape a model client expects, so an existing
// tool surfaces it rather than choking on it.
func (s *Server) refuse(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`+"\n", "osanwe_"+kind, message)
}

func (s *Server) refuseToken(w http.ResponseWriter, status int, kind, outcome, message string) {
	w.Header().Set(TokenOutcomeHeader, outcome)
	s.refuse(w, status, kind, message)
}

// handleModels lists what this gateway carries, in the shape a provider does.
func (s *Server) handleModels(w http.ResponseWriter) {
	models := s.modelNames()
	out := struct {
		Data []map[string]string `json:"data"`
	}{Data: make([]map[string]string, 0, len(models))}
	for _, m := range models {
		// The upstream address and its credential are deliberately absent: a
		// client has no use for either, and either would be worth stealing.
		// Which vendor serves a model is not a secret and could not be kept
		// one -- the names say so themselves.
		out.Data = append(out.Data, map[string]string{"id": m, "type": "model"})
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

// Metrics returns the counters.
func (s *Server) Metrics() *Metrics { return &s.metrics }

// Handler exposes the gateway as an http.Handler, for tests and for embedding.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.ServeHTTP) }

// Addr returns the address actually bound.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Listen binds without serving, so a caller can learn the assigned port.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("gateway: listening on %s: %w", s.cfg.Addr, err)
	}
	s.listener = ln
	return nil
}

// Serve accepts requests until shutdown. It calls Listen if needed.
//
// TLS is the caller's to provide, because a gateway is reached through a relay
// and the relay must not be able to read what it carries. Serving this
// unencrypted on anything but loopback would hand every prompt to the hop in
// front of it.
func (s *Server) Serve(tlsConf *tls.Config) error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	ln := s.listener
	if tlsConf != nil {
		ln = tls.NewListener(ln, tlsConf)
	}
	err := s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the server, waiting for in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
