package gateway

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
)

// A gateway can front several providers, choosing by the model asked for.
//
// This is what makes one endpoint and one credential worth having: a client
// says "claude-sonnet-5" or "deepseek-chat" and does not need an account,
// a key or a base URL for either. The provider relationships live here,
// which is the only place they can live if the client is not to hold them.
//
// # Why unknown models are refused
//
// The tempting default is to forward anything unrecognised to a primary
// provider. That turns a typo into a charge: a client asking for a model this
// gateway does not carry would spend a token, reach a provider that has never
// heard of the name, and get an error -- having paid for it. Refusing costs
// nothing and says what is actually available.

// Style is how a provider expects its credential. Nearly every hosted API is
// one of these two; the third format, Google's, puts the model in the URL
// path and needs more than a header swap, so it is deliberately absent rather
// than half-supported.
type Style string

const (
	// StyleAnthropic sends "x-api-key: KEY".
	StyleAnthropic Style = "anthropic"

	// StyleOpenAI sends "authorization: Bearer KEY", which DeepSeek, GLM,
	// Groq, Together, Fireworks, xAI, Mistral and Ollama all also speak.
	StyleOpenAI Style = "openai"
)

// MaxRoutedBody bounds a translated provider response held for conversion.
// Incoming requests use Config.MaxRequestBody, whose default is smaller.
const MaxRoutedBody = 16 << 20

// Route sends one model to one provider.
type Route struct {
	Model    string
	Style    Style
	Upstream string
	Cost     CostRates

	// Credential is the key itself, read from the environment rather than
	// stored in the route file: a routing table is the sort of thing that ends
	// up in version control, and a key in it is a key published.
	Credential string

	// CredentialEnv records where the credential came from, for diagnostics
	// that must never print the credential.
	CredentialEnv string
}

func (r Route) credential() Credential {
	switch r.Style {
	case StyleOpenAI:
		return Credential{Header: "authorization", Prefix: "Bearer ", Value: r.Credential}
	default:
		return Credential{Header: "x-api-key", Value: r.Credential}
	}
}

// Routes is a model-to-provider table.
type Routes struct {
	byModel map[string]Route
	order   []string
}

// NewRoutes builds a table, checking each entry.
func NewRoutes(list []Route) (*Routes, error) {
	rt := &Routes{byModel: make(map[string]Route, len(list))}
	for _, r := range list {
		switch {
		case strings.TrimSpace(r.Model) == "":
			return nil, fmt.Errorf("gateway: a route has no model name")
		case r.Style != StyleAnthropic && r.Style != StyleOpenAI:
			return nil, fmt.Errorf("gateway: route %q has unknown style %q; use %q or %q",
				r.Model, r.Style, StyleAnthropic, StyleOpenAI)
		case r.Credential == "":
			return nil, fmt.Errorf("gateway: route %q has no credential; set %s in the environment",
				r.Model, r.CredentialEnv)
		}
		if err := r.Cost.validOptional(); err != nil {
			return nil, fmt.Errorf("gateway: route %q has invalid prices: %w", r.Model, err)
		}
		if err := r.credential().valid(); err != nil {
			return nil, fmt.Errorf("gateway: route %q credential is invalid: %w", r.Model, err)
		}
		u, err := url.Parse(r.Upstream)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("gateway: route %q has an unusable upstream %q", r.Model, r.Upstream)
		}
		if u.Scheme != "https" {
			return nil, fmt.Errorf("gateway: route %q must use https, got %q; the pooled credential would otherwise cross the network in the clear",
				r.Model, r.Upstream)
		}
		if u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("gateway: route %q upstream must not contain user information, a query, or a fragment", r.Model)
		}
		if _, dup := rt.byModel[r.Model]; dup {
			return nil, fmt.Errorf("gateway: model %q is routed twice", r.Model)
		}
		rt.byModel[r.Model] = r
		rt.order = append(rt.order, r.Model)
	}
	if len(rt.byModel) == 0 {
		return nil, fmt.Errorf("gateway: the route table is empty")
	}
	sort.Strings(rt.order)
	return rt, nil
}

// Lookup finds the route for a model.
func (rt *Routes) Lookup(model string) (Route, bool) {
	r, ok := rt.byModel[model]
	return r, ok
}

// Models lists what this gateway carries, in a stable order.
func (rt *Routes) Models() []string {
	return append([]string(nil), rt.order...)
}

// AllPriced reports whether every route has both input and output pricing.
func (rt *Routes) AllPriced() bool {
	for _, route := range rt.byModel {
		if !route.Cost.priced() {
			return false
		}
	}
	return len(rt.byModel) > 0
}

// ParseRoutes reads a route table.
//
// The format is one route per line, whitespace separated:
//
//	# model          style      upstream                   credential env       optional input/output USD per 1M
//	claude-sonnet-5  anthropic  https://api.anthropic.com  ANTHROPIC_API_KEY    3.00 15.00
//	deepseek-chat    openai     https://api.deepseek.com   DEEPSEEK_API_KEY     0.27 1.10
//
// The fourth field names an environment variable, never the key itself. A file
// like this belongs in version control; a key in it does not.
func ParseRoutes(r io.Reader, lookupEnv func(string) string) (*Routes, error) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}

	var list []Route
	var missing []string
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 4 && len(fields) != 6 {
			return nil, fmt.Errorf("gateway: route file line %d has %d fields, want 4 or 6: model style upstream ENV_VAR [INPUT_USD_PER_MILLION OUTPUT_USD_PER_MILLION]",
				line, len(fields))
		}
		var cost CostRates
		if len(fields) == 6 {
			input, err := ParseCurrencyMicros(fields[4])
			if err != nil {
				return nil, fmt.Errorf("gateway: route file line %d has invalid input price: %w", line, err)
			}
			output, err := ParseCurrencyMicros(fields[5])
			if err != nil {
				return nil, fmt.Errorf("gateway: route file line %d has invalid output price: %w", line, err)
			}
			cost = CostRates{InputMicrosPerMillion: input, OutputMicrosPerMillion: output}
		}
		env := fields[3]
		value := lookupEnv(env)
		if value == "" {
			// Collected rather than returned one at a time, so an operator
			// setting up five providers learns about all five at once.
			missing = append(missing, fmt.Sprintf("%s (for %s)", env, fields[0]))
			continue
		}
		list = append(list, Route{
			Model:         fields[0],
			Style:         Style(fields[1]),
			Upstream:      fields[2],
			Cost:          cost,
			Credential:    value,
			CredentialEnv: env,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gateway: reading routes: %w", err)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("gateway: no credential in the environment for: %s", strings.Join(missing, ", "))
	}
	return NewRoutes(list)
}

// replay returns a body the proxy can send.
func replay(buf []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(buf)) }
