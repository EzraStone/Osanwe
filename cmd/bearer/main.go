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
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/bearer"
	"github.com/EzraStone/osanwe/internal/directory"
	"github.com/EzraStone/osanwe/internal/pool"
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
	var dirURLs, authKeys stringList

	fs := flag.NewFlagSet("bearer", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "loopback address to listen on")
	relay := fs.String("relay", "", "ranger address as host:port. Required")
	pin := fs.String("pin", "", "the ranger's public-key pin, as printed by `ranger -pin`. Required")
	secret := fs.String("secret", "", "shared secret for the relay (or set OSANWE_SECRET)")
	fs.Var(&dirURLs, "directory", "directory URL to fetch a consensus from (repeatable). An alternative to -relay/-pin")
	fs.Var(&authKeys, "authority", "trusted directory authority key (repeatable)")
	threshold := fs.Int("threshold", 2, "how many authorities must have signed the consensus")
	upstream := fs.String("upstream", bearer.DefaultUpstream, "provider base URL")
	upstreamCA := fs.String("upstream-ca", "", "PEM file of extra roots for verifying the provider. For a self-hosted gateway with a private CA; there is deliberately no option to skip verification")
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

	srv, err := bearer.New(bearer.Config{
		Addr:             *addr,
		Upstream:         *upstream,
		Dialer:           dialer,
		UpstreamRootCAs:  roots,
		AllowNonLoopback: *allowExposed,
		Logger:           log,
	})
	if err != nil {
		return err
	}
	if err := srv.Listen(); err != nil {
		return err
	}

	if relays != nil {
		// No relay has been chosen yet: the pool picks one on the first
		// request, so naming one here would be a guess.
		log.Info("bearer listening", "addr", srv.Addr().String(), "relays", relays.Len(), "upstream", *upstream)
	} else {
		log.Info("bearer listening", "addr", srv.Addr().String(), "relay", relayAddr, "upstream", *upstream)
	}
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
