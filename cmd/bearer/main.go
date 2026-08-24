// Command bearer runs the Osanwë client.
//
// It listens on loopback and forwards to a provider through a ranger. Point an
// existing tool at it by changing one base URL:
//
//	bearer -relay relay.example:8443 -pin sha256/... -secret "$SECRET"
//	export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
//
// Your API key never touches this process's configuration -- it travels in the
// request, inside TLS, to the provider. Phase 2 is bring-your-own-key: the
// provider still knows which account is asking, it just no longer learns where
// the request came from.
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/bearer"
	"github.com/EzraStone/osanwe/internal/directory"
	"github.com/EzraStone/osanwe/internal/mint"
	"github.com/EzraStone/osanwe/internal/pool"
	"github.com/EzraStone/osanwe/internal/tunnel"
	"github.com/EzraStone/osanwe/internal/version"
)

func main() {
	if err := run(); err != nil {
		// Library errors already carry a package prefix; adding another
		// here would print "cmd: cmd: ...".
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var dirURLs, authKeys, chatModels stringList

	fs := flag.NewFlagSet("bearer", flag.ContinueOnError)
	configPath := fs.String("config", "", "JSON file of non-secret connection settings. Command-line flags override it")
	addr := fs.String("addr", "127.0.0.1:8080", "loopback address to listen on")
	relay := fs.String("relay", "", "ranger address as host:port. Required")
	pin := fs.String("pin", "", "the ranger's public-key pin, as printed by `ranger -pin`. Required")
	secret := fs.String("secret", "", "shared secret for the relay (or set OSANWE_SECRET)")
	fs.Var(&dirURLs, "directory", "directory URL to fetch a consensus from (repeatable). An alternative to -relay/-pin")
	fs.Var(&authKeys, "authority", "trusted directory authority key (repeatable)")
	threshold := fs.Int("threshold", 2, "how many authorities must have signed the consensus")
	upstream := fs.String("upstream", "", "provider or gateway base URL (defaults to Anthropic in BYOK mode; required with -mint)")
	apiStyle := fs.String("api-style", bearer.APIStyleAnthropic, "embedded-chat provider API: anthropic or openai")
	fs.Var(&chatModels, "model", "model to show in the embedded chat (repeatable; empty shows the upstream catalog)")
	upstreamCA := fs.String("upstream-ca", "", "PEM file of extra roots for verifying the provider. For a self-hosted gateway with a private CA; there is deliberately no option to skip verification")
	allowExposed := fs.Bool("allow-exposed", false, "permit binding a non-loopback address. Traffic between your tools and bearer is plaintext, so this puts prompts on the network in the clear")
	mintURL := fs.String("mint", "", "mint to buy tokens from. Switches to paying with tokens instead of your own API key, and -upstream must then be a gateway")
	mintKeyID := fs.String("mint-key-id", "", "the mint's key id, obtained anywhere other than the mint itself. Required with -mint")
	receipt := fs.String("receipt", "", "proof of payment to present to the mint (or set OSANWE_RECEIPT)")
	noUI := fs.Bool("no-ui", false, "do not serve the local interface")
	openUI := fs.Bool("open-ui", false, "open the local interface in the default browser after startup")
	exitOnStdinClose := fs.Bool("exit-on-stdin-close", false, "shut down when the launcher's private stdin pipe closes")
	showVersion := fs.Bool("version", false, "print version and exit")
	buyToken := fs.Bool("buy-token", false, "buy one token, print it and exit. Needs -mint and -mint-key-id, and nothing else")
	verbose := fs.Bool("v", false, "verbose logging")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "bearer — the Osanwë client.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  bearer -relay HOST:PORT -pin sha256/... -secret SECRET\n\n"+
			"Then point your tool at it:\n  export ANTHROPIC_BASE_URL=http://127.0.0.1:8080\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	envSecret, envReceipt, err := consumeLauncherEnvironment()
	if err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version.String("bearer"))
		return nil
	}
	if *configPath != "" {
		cfg, err := loadClientFileConfig(*configPath)
		if err != nil {
			return err
		}
		set := explicitlySetFlags(fs)
		if !set["relay"] {
			*relay = cfg.Relay
		}
		if !set["pin"] {
			*pin = cfg.Pin
		}
		if !set["directory"] {
			dirURLs = append(dirURLs[:0], cfg.Directories...)
		}
		if !set["authority"] {
			authKeys = append(authKeys[:0], cfg.Authorities...)
		}
		if !set["threshold"] && cfg.Threshold != nil {
			*threshold = *cfg.Threshold
		}
		if !set["upstream"] {
			*upstream = cfg.Upstream
		}
		if !set["api-style"] && cfg.APIStyle != "" {
			*apiStyle = cfg.APIStyle
		}
		if !set["model"] {
			chatModels = append(chatModels[:0], cfg.Models...)
		}
		if !set["upstream-ca"] {
			*upstreamCA = cfg.UpstreamCA
		}
		if !set["mint"] {
			*mintURL = cfg.Mint
		}
		if !set["mint-key-id"] {
			*mintKeyID = cfg.MintKeyID
		}
	}
	if *openUI && *noUI {
		return errors.New("bearer: -open-ui cannot be combined with -no-ui")
	}

	// Buying a single token needs no relay, no secret and no listener, so it
	// is handled before any of those are required. It exists so a token can be
	// held in a shell variable and inspected, which is the only way to
	// demonstrate by hand that spending one twice does not work.
	if *buyToken {
		if *mintURL == "" || *mintKeyID == "" {
			return errors.New("bearer: -buy-token needs -mint and -mint-key-id")
		}
		rcpt := envReceipt
		if *receipt != "" {
			rcpt = *receipt
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tok, err := (&mint.Client{URL: *mintURL, ExpectKeyID: *mintKeyID}).Token(ctx, rcpt)
		if err != nil {
			return err
		}
		fmt.Println(tok.Encode())
		return nil
	}
	if *mintURL != "" && strings.TrimSpace(*upstream) == "" {
		return errors.New("bearer: -mint requires an explicit gateway -upstream; the default Anthropic provider does not accept Osanwe tokens")
	}

	usingDirectory := len(dirURLs) > 0
	if usingDirectory && (*relay != "" || *pin != "") {
		return errors.New("bearer: use either -relay/-pin or -directory, not both. " +
			"A manual pin is the stronger option and silently preferring one would hide which was in force")
	}
	if !usingDirectory {
		if *relay == "" {
			return errors.New("bearer: no -relay given. Ask a relay operator for its address and pin, or use -directory")
		}
		if *pin == "" {
			return errors.New("bearer: no -pin given. The relay operator gets it from `ranger -pin`; without it the relay is unauthenticated and could be substituted")
		}
	}

	// Environment first: a command line is visible in the process table to
	// every user on the machine.
	sec := envSecret
	if *secret != "" {
		sec = *secret
	}
	if sec == "" {
		return errors.New("bearer: no secret set. Put the relay's secret in OSANWE_SECRET")
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// runCtx bounds the directory refresh loop, which lives as long as the
	// process does.
	runCtx, stopRefresh := context.WithCancel(context.Background())
	defer stopRefresh()

	var dialer bearer.Dialer
	var relays *pool.Pool

	relayAddr, relayPin := *relay, *pin
	if usingDirectory {
		if len(authKeys) == 0 {
			return errors.New("bearer: -directory needs at least one -authority key. " +
				"Without one, any server answering the URL could hand you a relay it controls")
		}
		if *threshold > len(authKeys) {
			return fmt.Errorf("bearer: -threshold %d exceeds the %d authority keys given, so no consensus could satisfy it",
				*threshold, len(authKeys))
		}
		if *threshold < 2 && len(authKeys) > 1 {
			log.Warn("threshold is 1 although several authorities are configured; a single compromised authority can choose your relay")
		}
		authorities, err := directory.AuthoritySet(authKeys)
		if err != nil {
			return err
		}

		probe, err := bearer.New(bearer.Config{Addr: *addr, Upstream: *upstream, Dialer: noopDialer{}, AllowNonLoopback: *allowExposed})
		if err != nil {
			return err
		}
		want := probe.UpstreamAddr()

		relays, err = pool.New(pool.Config{
			Fetcher:     &directory.Fetcher{URLs: dirURLs, Authorities: authorities, Threshold: *threshold},
			Destination: want,
			Secret:      sec,
			Logger:      log,
		})
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(runCtx, 30*time.Second)
		err = relays.Refresh(ctx)
		cancel()
		if err != nil {
			return err
		}

		// From here the pool keeps itself current: it re-fetches on a timer and
		// moves to another relay when the one in use stops answering, so a
		// relay going down is not something the user has to notice.
		go relays.Run(runCtx)

		log.Info("relays available from the directory", "count", relays.Len(), "destination", want)
		dialer = relays
	} else {
		d, err := tunnel.New(tunnel.Config{Relay: relayAddr, Pin: relayPin, Secret: sec})
		if err != nil {
			return err
		}
		dialer = d
	}

	var roots *x509.CertPool
	if *upstreamCA != "" {
		pem, err := os.ReadFile(*upstreamCA)
		if err != nil {
			return fmt.Errorf("bearer: reading -upstream-ca: %w", err)
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return fmt.Errorf("bearer: -upstream-ca %s contained no usable certificates", *upstreamCA)
		}
		log.Info("using extra roots to verify the provider", "file", *upstreamCA)
	}

	// Paying with tokens is opt-in and, once on, changes what the upstream
	// must be: a provider would reject a token and has no idea what one is.
	var wallet *mint.Wallet
	if *mintURL != "" {
		if *mintKeyID == "" {
			return errors.New("bearer: -mint needs -mint-key-id. " +
				"Take the id from somewhere other than the mint: a mint that handed every buyer a key of their own " +
				"would put each of them in an anonymity set of one while appearing to work perfectly")
		}
		rcpt := envReceipt
		if *receipt != "" {
			rcpt = *receipt
		}
		wallet = mint.NewWallet(&mint.Client{URL: *mintURL, ExpectKeyID: *mintKeyID}, rcpt, 8)
		go wallet.Run(runCtx)
		log.Info("paying with tokens", "mint", *mintURL, "key", *mintKeyID)
	} else if *mintKeyID != "" {
		return errors.New("bearer: -mint-key-id given without -mint, so no tokens would be bought and your own API key would still be used")
	}

	cfg := bearer.Config{
		Addr:             *addr,
		Upstream:         *upstream,
		APIStyle:         *apiStyle,
		Models:           append([]string(nil), chatModels...),
		Dialer:           dialer,
		UpstreamRootCAs:  roots,
		AllowNonLoopback: *allowExposed,
		UI:               !*noUI,
		Logger:           log,
	}
	if wallet != nil {
		cfg.Tokens = wallet
	}
	if relays != nil {
		cfg.Relays = relays
	} else {
		cfg.ManualRelay = relayAddr
	}
	srv, err := bearer.New(cfg)
	if err != nil {
		return err
	}
	if err := srv.Listen(); err != nil {
		return err
	}

	if relays != nil {
		// No relay has been chosen yet: the pool picks one on the first
		// request, so naming one here would be a guess.
		log.Info("bearer listening", "addr", srv.Addr().String(), "relays", relays.Len(), "upstream", srv.UpstreamAddr())
	} else {
		log.Info("bearer listening", "addr", srv.Addr().String(), "relay", relayAddr, "upstream", srv.UpstreamAddr())
	}
	if !*noUI {
		fmt.Fprintf(os.Stderr, "\n  Open this:\n    http://%s%s\n", srv.Addr().String(), bearer.Prefix)
	}
	fmt.Fprintf(os.Stderr, "\n  Point your tool at this:\n    export ANTHROPIC_BASE_URL=http://%s\n\n"+
		"  The relay must allow %s; its operator sets that with -allow.\n\n",
		srv.Addr().String(), srv.UpstreamAddr())
	if wallet != nil {
		fmt.Fprintf(os.Stderr, "  Each accepted inference request spends one token; the model catalog is free. Your own API key is not used\n"+
			"  and is stripped before anything leaves this machine.\n\n")
	}
	if *allowExposed {
		log.Warn("bound a non-loopback address; traffic between your tools and bearer is plaintext on the network")
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()
	var launcherClosed <-chan struct{}
	if *exitOnStdinClose {
		launcherClosed = stdinCloseSignal(os.Stdin)
	}
	if *openUI {
		localURL := "http://" + srv.Addr().String() + bearer.Prefix
		if err := openLocalBrowser(localURL); err != nil {
			log.Warn("could not open the local interface automatically", "url", localURL, "error", err)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		log.Info("shutting down", "signal", sig.String())
	case <-launcherClosed:
		log.Info("shutting down", "reason", "launcher closed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// stdinCloseSignal lets a desktop launcher own the client without putting a
// secret on the command line or leaving a terminal window open. The launcher
// gives bearer a private stdin pipe and keeps its write side open for exactly
// as long as the app window is alive. EOF therefore means the launcher closed
// or crashed, and the normal server shutdown path can run.
func stdinCloseSignal(r io.Reader) <-chan struct{} {
	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(closed)
	}()
	return closed
}

// consumeLauncherEnvironment copies the two launcher-only values into this
// process and immediately removes them from its environment. The client still
// holds the values it needs in memory, but helpers it starts later (including
// the default-browser opener) cannot inherit them.
func consumeLauncherEnvironment() (secret, receipt string, err error) {
	secret = os.Getenv("OSANWE_SECRET")
	receipt = os.Getenv("OSANWE_RECEIPT")
	for _, name := range []string{"OSANWE_SECRET", "OSANWE_RECEIPT"} {
		if unsetErr := os.Unsetenv(name); unsetErr != nil {
			return "", "", fmt.Errorf("bearer: removing %s from the child environment: %w", name, unsetErr)
		}
	}
	return secret, receipt, nil
}

func explicitlySetFlags(fs *flag.FlagSet) map[string]bool {
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
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

// noopDialer lets a Server be constructed purely to compute the upstream
// address a relay must serve, before any relay has been chosen.
type noopDialer struct{}

func (noopDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("bearer: no relay selected yet")
}
