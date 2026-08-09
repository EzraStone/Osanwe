// Command mithlond runs the Osanwë gateway.
//
// It takes blind-signed tokens instead of accounts, and calls the provider
// using a pooled credential the client never holds:
//
//	export OSANWE_PROVIDER_KEY=sk-ant-...
//	mithlond -mint-key mint.pub -spent-db spent.db -budget-db budget.db -models claude-sonnet-4 -upstream https://api.anthropic.com
//
// A relay in front of this hides who is asking. This hides what they are
// asking with. Neither component sits on both sides of that split, and the
// whole arrangement rests on the two not colluding.
//
// Read the package documentation for internal/gateway before running one in
// front of real users. This process reads prompts, the design calls for it to
// run in an attested enclave so that its operator provably cannot, and that
// part is not built.
package main

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/gateway"
	"github.com/EzraStone/osanwe/internal/mint"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var mintKeys stringList

	fs := flag.NewFlagSet("mithlond", flag.ContinueOnError)
	addr := fs.String("addr", "0.0.0.0:8444", "address to listen on")
	upstream := fs.String("upstream", "https://api.anthropic.com", "provider base URL")
	fs.Var(&mintKeys, "mint-key", "PEM file holding a mint's public key (repeatable, so a rotation can overlap)")
	credHeader := fs.String("credential-header", "x-api-key", "header the provider expects its credential in. Use `authorization` for OpenAI-compatible providers")
	credPrefix := fs.String("credential-prefix", "", "prefix before the credential, e.g. `Bearer ` for OpenAI-compatible providers")
	routesPath := fs.String("routes", "", "route table mapping models to providers. With one, -upstream and the single credential are unused")
	modelsCSV := fs.String("models", "", "comma-separated exact model allowlist for a single upstream (required without -routes)")
	maxRequestBytes := fs.Int64("max-request-bytes", gateway.DefaultMaxRequestBody, "largest request body accepted in bytes")
	maxOutputTokens := fs.Int("max-output-tokens", gateway.DefaultMaxOutputTokens, "largest max_tokens value accepted")
	upstreamCA := fs.String("upstream-ca", "", "PEM file of extra roots for verifying the provider. For a self-hosted provider with a private CA; there is deliberately no option to skip verification")
	certPath := fs.String("cert", "mithlond.crt", "TLS certificate path, created if absent")
	keyPath := fs.String("key", "mithlond.key", "TLS key path, created if absent")
	spentDB := fs.String("spent-db", "", "local durable spent-token journal (required; put it on a local filesystem, not NFS)")
	budgetDB := fs.String("budget-db", "", "local durable aggregate-budget database (required; put it on a local filesystem, not NFS)")
	budgetWindow := fs.Duration("budget-window", gateway.DefaultBudgetWindow, "fixed window for aggregate provider limits")
	budgetRequests := fs.Uint64("budget-requests", gateway.DefaultBudgetRequests, "maximum provider requests in one budget window")
	budgetInputBytes := fs.Uint64("budget-input-bytes", gateway.DefaultBudgetInputBytes, "maximum normalized input bytes reserved in one budget window")
	budgetOutputTokens := fs.Uint64("budget-output-tokens", gateway.DefaultBudgetOutputTokens, "maximum requested output tokens reserved in one budget window")
	budgetCostUSD := fs.String("budget-cost-usd", "", "optional conservative provider-cost ceiling per window in USD; requires model prices")
	inputUSDPerMillion := fs.String("input-usd-per-million", "", "single-upstream input price per million tokens; input bytes are conservatively treated as tokens")
	outputUSDPerMillion := fs.String("output-usd-per-million", "", "single-upstream output price per million tokens")
	hosts := fs.String("hosts", "", "comma-separated names or IPs for a generated certificate")
	plaintext := fs.Bool("insecure-plaintext", false, "serve without TLS. Only correct behind a TLS terminator you control; otherwise the relay in front reads every prompt")
	verbose := fs.Bool("v", false, "verbose logging")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "mithlond — the Osanwë gateway.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  export OSANWE_PROVIDER_KEY=sk-...\n"+
			"  mithlond -mint-key mint.pub -spent-db spent.db -budget-db budget.db -models MODEL -upstream https://api.anthropic.com\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*spentDB) == "" {
		return errors.New("mithlond: -spent-db is required; redemption state must survive restarts")
	}
	if strings.TrimSpace(*budgetDB) == "" {
		return errors.New("mithlond: -budget-db is required; a pooled provider account must have a durable aggregate spending ceiling")
	}
	var maxCostMicros uint64
	if *budgetCostUSD != "" {
		var err error
		maxCostMicros, err = gateway.ParseCurrencyMicros(*budgetCostUSD)
		if err != nil {
			return fmt.Errorf("mithlond: invalid -budget-cost-usd: %w", err)
		}
	}
	var cost gateway.CostRates
	if *routesPath != "" && (*inputUSDPerMillion != "" || *outputUSDPerMillion != "") {
		return errors.New("mithlond: single-upstream price flags cannot be used with -routes; put prices on each route")
	}
	if *routesPath == "" && (*inputUSDPerMillion != "" || *outputUSDPerMillion != "") {
		if *inputUSDPerMillion == "" || *outputUSDPerMillion == "" {
			return errors.New("mithlond: -input-usd-per-million and -output-usd-per-million must be set together")
		}
		input, err := gateway.ParseCurrencyMicros(*inputUSDPerMillion)
		if err != nil {
			return fmt.Errorf("mithlond: invalid -input-usd-per-million: %w", err)
		}
		output, err := gateway.ParseCurrencyMicros(*outputUSDPerMillion)
		if err != nil {
			return fmt.Errorf("mithlond: invalid -output-usd-per-million: %w", err)
		}
		cost = gateway.CostRates{InputMicrosPerMillion: input, OutputMicrosPerMillion: output}
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Environment, not a flag: a command line is visible in the process table
	// to every user on the machine, and these are the pooled credentials for
	// everybody the gateway serves.
	var routes *gateway.Routes
	providerKey := os.Getenv("OSANWE_PROVIDER_KEY")

	if *routesPath != "" && providerKey != "" {
		log.Warn("both -routes and OSANWE_PROVIDER_KEY are set; the route table wins and the single credential is unused")
	}
	if *routesPath != "" {
		f, err := os.Open(*routesPath)
		if err != nil {
			return fmt.Errorf("mithlond: opening -routes: %w", err)
		}
		routes, err = gateway.ParseRoutes(f, os.Getenv)
		f.Close()
		if err != nil {
			return err
		}
		log.Info("routing by model", "models", strings.Join(routes.Models(), ", "))
	} else if providerKey == "" {
		return errors.New("mithlond: no provider credential. Put one in OSANWE_PROVIDER_KEY, " +
			"or pass -routes to front several providers at once")
	}

	if len(mintKeys) == 0 {
		return errors.New("mithlond: no -mint-key given. A gateway must know which mint's tokens it honours, or it honours none")
	}
	keys := map[string]*rsa.PublicKey{}
	for _, path := range mintKeys {
		pub, err := mint.LoadPublicKey(path)
		if err != nil {
			return err
		}
		id := mint.KeyID(pub)
		if _, dup := keys[id]; dup {
			return fmt.Errorf("mithlond: %s holds a key already loaded (%s)", path, id)
		}
		keys[id] = pub
		log.Info("accepting tokens from mint key", "key", id, "file", path)
	}

	var roots *x509.CertPool
	if *upstreamCA != "" {
		pemBytes, err := os.ReadFile(*upstreamCA)
		if err != nil {
			return fmt.Errorf("mithlond: reading -upstream-ca: %w", err)
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pemBytes) {
			return fmt.Errorf("mithlond: -upstream-ca %s contained no usable certificates", *upstreamCA)
		}
		log.Info("using extra roots to verify the provider", "file", *upstreamCA)
	}

	var tlsConf *tls.Config
	if *plaintext {
		log.Warn("serving without TLS; whatever sits in front of this reads every prompt that passes through it")
	} else {
		names := []string{"localhost", "127.0.0.1"}
		if *hosts != "" {
			names = strings.Split(*hosts, ",")
		}
		cert, pin, created, err := certs.LoadOrCreate(*certPath, *keyPath, names, 365*24*time.Hour)
		if err != nil {
			return err
		}
		if created {
			log.Info("created a TLS identity", "cert", *certPath, "key", *keyPath)
		}
		tlsConf = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		fmt.Fprintf(os.Stderr, "\n  Gateway pin: %s\n", pin)
	}

	spent, err := mint.OpenFileSpentSet(*spentDB)
	if err != nil {
		return err
	}
	defer func() {
		if err := spent.Close(); err != nil {
			log.Error("closing spent-token database", "error", err)
		}
	}()
	budget, err := gateway.OpenFileBudget(gateway.FileBudgetConfig{
		Path: *budgetDB, Window: *budgetWindow,
		MaxRequests: *budgetRequests, MaxInputBytes: *budgetInputBytes, MaxOutputTokens: *budgetOutputTokens,
		MaxCostMicros: maxCostMicros,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := budget.Close(); err != nil {
			log.Error("closing aggregate-budget database", "error", err)
		}
	}()

	srv, err := gateway.New(gateway.Config{
		Addr:             *addr,
		Upstream:         *upstream,
		MintKeys:         keys,
		Spent:            spent,
		Budget:           budget,
		Models:           commaList(*modelsCSV),
		MaxRequestBody:   *maxRequestBytes,
		MaxOutputTokens:  *maxOutputTokens,
		Routes:           routes,
		Cost:             cost,
		RequireCostRates: maxCostMicros > 0,
		Credential:       gateway.Credential{Header: *credHeader, Prefix: *credPrefix, Value: providerKey},
		UpstreamRootCAs:  roots,
		Logger:           log,
	})
	if err != nil {
		return err
	}
	if err := srv.Listen(); err != nil {
		return err
	}

	log.Info("mithlond listening", "addr", srv.Addr().String(), "upstream", *upstream,
		"mint_keys", len(keys), "spent_db", *spentDB, "budget_db", *budgetDB,
		"budget_window", budgetWindow.String(), "budget_requests", *budgetRequests,
		"budget_input_bytes", *budgetInputBytes,
		"budget_output_tokens", *budgetOutputTokens,
		"budget_cost_usd", *budgetCostUSD)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(tlsConf) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		log.Info("shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func commaList(value string) []string {
	var values []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

// stringList collects a repeatable, comma-separated flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}
