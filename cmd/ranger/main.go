// Command ranger runs an Osanwë relay node.
//
// A ranger forwards an encrypted tunnel it cannot read. Running one is how you
// contribute to the network; it needs a small VPS, not a GPU.
//
//	ranger -allow api.anthropic.com -secret "$(ranger -gen-secret)"
//
// On first start it generates a TLS identity, saves it, and prints the pin
// clients need. Give the address and pin to whoever is using the relay.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/certs"
	"github.com/EzraStone/osanwe/internal/directory"
	"github.com/EzraStone/osanwe/internal/policy"
	"github.com/EzraStone/osanwe/internal/ranger"
	"github.com/EzraStone/osanwe/internal/version"
)

// certValidity is long because rotating a self-signed identity means telling
// every client a new pin. Renewal is a coordination cost, not a security one:
// the key, not the expiry, is what clients verify.
const certValidity = 10 * 365 * 24 * time.Hour

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

func main() {
	if err := run(); err != nil {
		// Library errors already carry a package prefix; adding another
		// here would print "cmd: cmd: ...".
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var allow stringList

	fs := flag.NewFlagSet("ranger", flag.ContinueOnError)
	fs.Var(&allow, "allow", "destination this relay may carry traffic to, as host or host:port (repeatable, comma-separated). Required")
	addr := fs.String("addr", ":8443", "address to listen on")
	metricsAddr := fs.String("metrics", "127.0.0.1:9464", "address for the metrics endpoint; empty disables it")
	secret := fs.String("secret", "", "shared secret clients must present (or set OSANWE_RANGER_SECRET)")
	dir := fs.String("dir", ".", "directory holding the relay's TLS identity")
	certPath := fs.String("cert", "", "TLS certificate path (default <dir>/ranger.crt)")
	keyPath := fs.String("key", "", "TLS key path (default <dir>/ranger.key)")
	logDest := fs.Bool("log-destinations", false, "log which destination each connection requested. Off by default: a relay that records who talked to which provider builds the correlation log this network exists to prevent")
	verbose := fs.Bool("v", false, "verbose logging")
	nickname := fs.String("nickname", "", "short name for this relay in the directory. Enables descriptor publication")
	contact := fs.String("contact", "", "operator contact published in the descriptor (optional)")
	descOut := fs.String("descriptor", "", "write a signed descriptor to this path and exit")
	publishTo := fs.String("publish", "", "POST a signed descriptor to this authority's /publish endpoint and exit (repeatable, comma-separated)")
	descValid := fs.Duration("descriptor-validity", 24*time.Hour, "how long a published descriptor stays valid")
	advertise := fs.String("advertise", "", "address clients should dial, if different from -addr")
	genSecret := fs.Bool("gen-secret", false, "print a fresh random secret and exit")
	showIdentity := fs.Bool("identity", false, "print this relay's directory identity and exit")
	showPin := fs.Bool("pin", false, "print the relay's pin and exit")
	showVersion := fs.Bool("version", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "ranger — an Osanwë relay node.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  ranger -allow api.anthropic.com -secret SECRET\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println(version.String("ranger"))
		return nil
	}

	if *genSecret {
		s, err := auth.GenerateSecret()
		if err != nil {
			return err
		}
		fmt.Println(s)
		return nil
	}

	if *certPath == "" {
		*certPath = filepath.Join(*dir, "ranger.crt")
	}
	if *keyPath == "" {
		*keyPath = filepath.Join(*dir, "ranger.key")
	}
	identityPath := filepath.Join(*dir, "ranger.identity")

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cert, pin, created, err := certs.LoadOrCreate(*certPath, *keyPath, identityHosts(*addr), certValidity)
	if err != nil {
		return err
	}

	if *showPin {
		fmt.Println(pin)
		return nil
	}

	// The directory identity is separate from the TLS key: the certificate can
	// be rotated routinely, but this key is what clients remember.
	identity, idCreated, err := directory.LoadOrCreateIdentity(identityPath)
	if err != nil {
		return err
	}
	if *showIdentity {
		fmt.Println(identity.Fingerprint())
		return nil
	}
	if idCreated {
		log.Info("generated a new directory identity", "path", identityPath)
	}

	if *descOut != "" || *publishTo != "" {
		if len(allow) == 0 {
			return errors.New("ranger: -descriptor needs -allow, since a descriptor must say what the relay will carry")
		}
		if *nickname == "" {
			return errors.New("ranger: -descriptor needs -nickname")
		}
		allowlist, err := policy.Parse(allow)
		if err != nil {
			return err
		}
		advertised := *advertise
		if advertised == "" {
			advertised = displayAddr(*addr)
		}
		now := time.Now()
		d := &directory.Descriptor{
			Nickname:     *nickname,
			Address:      advertised,
			TLSPin:       pin,
			Identity:     identity.Fingerprint(),
			Destinations: allowlist.Destinations(),
			Contact:      *contact,
			Published:    now,
			Expires:      now.Add(*descValid),
		}
		encoded, err := d.Sign(identity)
		if err != nil {
			return err
		}
		if strings.Contains(advertised, "<this-host>") {
			return errors.New("ranger: -advertise is required when the relay binds a wildcard address, " +
				"since the descriptor must carry an address clients can actually dial")
		}

		if *descOut != "" {
			if err := os.WriteFile(*descOut, encoded, 0o644); err != nil {
				return fmt.Errorf("ranger: writing descriptor: %w", err)
			}
			fmt.Fprintf(os.Stderr, "wrote descriptor for %q (%s) to %s\n", d.Nickname, d.Address, *descOut)
		}

		if *publishTo != "" {
			var failures int
			for _, raw := range strings.Split(*publishTo, ",") {
				endpoint := strings.TrimSpace(raw)
				if endpoint == "" {
					continue
				}
				if err := publish(endpoint, encoded); err != nil {
					// Publishing to several authorities is normal, and one
					// being down must not stop the others from hearing about
					// this relay.
					fmt.Fprintf(os.Stderr, "  %s: %v\n", endpoint, err)
					failures++
					continue
				}
				fmt.Fprintf(os.Stderr, "  %s: accepted\n", endpoint)
			}
			if failures > 0 {
				return fmt.Errorf("ranger: %d authority endpoint(s) refused or were unreachable", failures)
			}
		}
		return nil
	}

	// Secret from the environment by preference: a command line is visible in
	// the process table to every user on the machine.
	sec := os.Getenv("OSANWE_RANGER_SECRET")
	if *secret != "" {
		sec = *secret
	}
	if sec == "" {
		return errors.New("ranger: no secret set. Generate one with `ranger -gen-secret` and pass it in OSANWE_RANGER_SECRET")
	}
	authenticator, err := auth.New(sec)
	if err != nil {
		return err
	}

	if len(allow) == 0 {
		return errors.New("ranger: no -allow destinations given. A relay must be told what it may carry traffic to; it will not forward to arbitrary hosts")
	}
	allowlist, err := policy.Parse(allow)
	if err != nil {
		return err
	}

	srv, err := ranger.New(ranger.Config{
		Addr: *addr,
		TLS: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
		Allowlist:       allowlist,
		Auth:            authenticator,
		Logger:          log,
		LogDestinations: *logDest,
	})
	if err != nil {
		return err
	}
	if err := srv.Listen(); err != nil {
		return err
	}

	if created {
		log.Info("generated a new relay identity", "cert", *certPath, "key", *keyPath)
	}
	log.Info("ranger listening", "addr", srv.Addr().String(), "destinations", allowlist.Destinations())
	fmt.Fprintf(os.Stderr, "\n  pin: %s\n\n  Give clients the address and this pin:\n    bearer -relay %s -pin %s\n\n",
		pin, displayAddr(srv.Addr().String()), pin)
	if *logDest {
		log.Warn("destination logging is on; this records which provider each connection used. Turn it off when finished debugging")
	}

	var metricsSrv *http.Server
	if *metricsAddr != "" {
		metricsSrv = startMetrics(*metricsAddr, srv, log)
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
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(ctx)
	}
	return srv.Shutdown(ctx)
}

// startMetrics serves cumulative counters. It binds loopback by default: these
// numbers describe a relay's traffic and there is no reason to publish them.
func startMetrics(addr string, srv *ranger.Server, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		m := srv.Metrics()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		for _, row := range []struct {
			name, help string
			value      int64
		}{
			{"osanwe_ranger_accepted_total", "Requests accepted for handling.", m.Accepted.Load()},
			{"osanwe_ranger_auth_failed_total", "Requests rejected for bad or missing credentials.", m.AuthFailed.Load()},
			{"osanwe_ranger_policy_denied_total", "Requests rejected for an unlisted destination.", m.PolicyDenied.Load()},
			{"osanwe_ranger_bad_request_total", "Requests that were not usable CONNECTs.", m.BadRequest.Load()},
			{"osanwe_ranger_dial_failed_total", "Upstream dials that failed.", m.DialFailed.Load()},
			{"osanwe_ranger_tunnels_total", "Tunnels established.", m.Tunnels.Load()},
			{"osanwe_ranger_tunnels_active", "Tunnels currently open.", m.TunnelsActive.Load()},
			{"osanwe_ranger_bytes_to_target_total", "Bytes forwarded toward providers.", m.BytesToTarget.Load()},
			{"osanwe_ranger_bytes_to_client_total", "Bytes forwarded toward clients.", m.BytesToClient.Load()},
		} {
			fmt.Fprintf(w, "# HELP %s %s\n", row.name, row.help)
			kind := "counter"
			if strings.HasSuffix(row.name, "_active") {
				kind = "gauge"
			}
			fmt.Fprintf(w, "# TYPE %s %s\n%s %d\n", row.name, kind, row.name, row.value)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	s := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("metrics endpoint stopped", "error", err)
		}
	}()
	log.Info("metrics listening", "addr", addr)
	return s
}

// identityHosts returns the names to put in a generated certificate. Clients
// verify by pin rather than by name, so this only has to be non-empty and
// sensible; getting it wrong cannot weaken authentication.
func identityHosts(addr string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		hosts = append(hosts, host)
	}
	return hosts
}

// displayAddr turns a wildcard bind into something copy-pasteable.
func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		return net.JoinHostPort("<this-host>", port)
	}
	return addr
}

// publish POSTs a signed descriptor to a directory authority.
//
// The descriptor is signed, so this needs no credential and no transport
// security to be safe: an authority verifies the signature, and a middlebox
// that altered the document in flight would only produce something the
// authority rejects.
func publish(endpoint string, descriptor []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(descriptor))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("User-Agent", "osanwe-ranger")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := strings.TrimSpace(string(body))
	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("not admitted by this authority (403). Its operator must add this relay's identity to their accept list: %s", msg)
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("the authority already holds a newer descriptor (409): %s", msg)
	case resp.StatusCode == http.StatusServiceUnavailable:
		return fmt.Errorf("this authority does not accept submissions (503): %s", msg)
	default:
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
}
