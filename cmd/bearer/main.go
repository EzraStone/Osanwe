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
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/bearer"
	"github.com/EzraStone/osanwe/internal/tunnel"
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
	fs := flag.NewFlagSet("bearer", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "loopback address to listen on")
	relay := fs.String("relay", "", "ranger address as host:port. Required")
	pin := fs.String("pin", "", "the ranger's public-key pin, as printed by `ranger -pin`. Required")
	secret := fs.String("secret", "", "shared secret for the relay (or set OSANWE_SECRET)")
	upstream := fs.String("upstream", bearer.DefaultUpstream, "provider base URL")
	allowExposed := fs.Bool("allow-exposed", false, "permit binding a non-loopback address. Traffic between your tools and bearer is plaintext, so this puts prompts on the network in the clear")
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

	if *relay == "" {
		return errors.New("bearer: no -relay given. Ask a relay operator for its address and pin")
	}
	if *pin == "" {
		return errors.New("bearer: no -pin given. The relay operator gets it from `ranger -pin`; without it the relay is unauthenticated and could be substituted")
	}

	// Environment first: a command line is visible in the process table to
	// every user on the machine.
	sec := os.Getenv("OSANWE_SECRET")
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

	dialer, err := tunnel.New(tunnel.Config{Relay: *relay, Pin: *pin, Secret: sec})
	if err != nil {
		return err
	}

	srv, err := bearer.New(bearer.Config{
		Addr:             *addr,
		Upstream:         *upstream,
		Dialer:           dialer,
		AllowNonLoopback: *allowExposed,
		Logger:           log,
	})
	if err != nil {
		return err
	}
	if err := srv.Listen(); err != nil {
		return err
	}

	log.Info("bearer listening", "addr", srv.Addr().String(), "relay", *relay, "upstream", *upstream)
	fmt.Fprintf(os.Stderr, "\n  Point your tool at this:\n    export ANTHROPIC_BASE_URL=http://%s\n\n"+
		"  The relay must allow %s; its operator sets that with -allow.\n\n",
		srv.Addr().String(), srv.UpstreamAddr())
	if *allowExposed {
		log.Warn("bound a non-loopback address; traffic between your tools and bearer is plaintext on the network")
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

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
