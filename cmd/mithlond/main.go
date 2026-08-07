// Command mithlond runs the Osanwë gateway.
//
// It takes blind-signed tokens instead of accounts, and calls the provider
// using a pooled credential the client never holds:
//
//	export OSANWE_PROVIDER_KEY=sk-ant-...
//	mithlond -mint-key mint.pub -upstream https://api.anthropic.com
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
	certPath := fs.String("cert", "mithlond.crt", "TLS certificate path, created if absent")
	keyPath := fs.String("key", "mithlond.key", "TLS key path, created if absent")
	hosts := fs.String("hosts", "", "comma-separated names or IPs for a generated certificate")
	plaintext := fs.Bool("insecure-plaintext", false, "serve without TLS. Only correct behind a TLS terminator you control; otherwise the relay in front reads every prompt")
	verbose := fs.Bool("v", false, "verbose logging")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "mithlond — the Osanwë gateway.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  export OSANWE_PROVIDER_KEY=sk-...\n"+
			"  mithlond -mint-key mint.pub -upstream https://api.anthropic.com\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Environment, not a flag: a command line is visible in the process table
	// to every user on the machine, and this one is the pooled credential for
	// everybody the gateway serves.
	providerKey := os.Getenv("OSANWE_PROVIDER_KEY")
	if providerKey == "" {
		return errors.New("mithlond: no provider credential. Put it in OSANWE_PROVIDER_KEY")
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

	srv, err := gateway.New(gateway.Config{
		Addr:       *addr,
		Upstream:   *upstream,
		MintKeys:   keys,
		Spent:      mint.NewSpentSet(),
		Credential: gateway.Credential{Header: *credHeader, Prefix: *credPrefix, Value: providerKey},
		Logger:     log,
	})
	if err != nil {
		return err
	}
	if err := srv.Listen(); err != nil {
		return err
	}

	log.Info("mithlond listening", "addr", srv.Addr().String(), "upstream", *upstream, "mint_keys", len(keys))

	// The spent set is in memory, which is a real limit and not a detail to
	// discover in production: restarting the gateway makes every token issued
	// before the restart spendable again, and two gateways sharing a mint do
	// not see each other's redemptions.
	log.Warn("spent tokens are held in memory only; a restart makes every previously spent token valid again, " +
		"and running more than one gateway needs shared state for this")

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
