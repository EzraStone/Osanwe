// Command eregion runs the Osanwë mint.
//
// It sells tokens and cannot recognise them afterwards:
//
//	eregion -key mint.key -publish mint.pub
//
// The mint learns who paid. It never learns what they asked, and cannot link
// a token it signed to the token that later turns up at a gateway -- not
// because the link is hard to compute, but because the blinding means the
// information was never there.
//
// Payment is deliberately not built in. -open issues to anyone who asks, which
// is right for a demo and wrong for anything else; a real deployment
// implements mint.Authorizer against whatever rail it sells through.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EzraStone/osanwe/internal/mint"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("eregion", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8445", "address to listen on")
	keyPath := fs.String("key", "mint.key", "signing key path, created if absent")
	publish := fs.String("publish", "", "also write the public key to this path, for gateway operators")
	bits := fs.Int("bits", mint.MinKeyBits, "key size when generating")
	open := fs.Bool("open", false, "issue a token to anyone who asks, with no payment. For demos only: an open mint prints money")
	printKey := fs.Bool("print-key-id", false, "print the key id and exit")
	verbose := fs.Bool("v", false, "verbose logging")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "eregion — the Osanwë mint.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  eregion -key mint.key -publish mint.pub -open\n\nFlags:\n")
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

	priv, created, err := mint.LoadOrCreateKey(*keyPath, *bits)
	if err != nil {
		return err
	}
	if created {
		log.Info("generated a signing key", "path", *keyPath, "bits", priv.N.BitLen())
	}

	if *printKey {
		fmt.Println(mint.KeyID(&priv.PublicKey))
		return nil
	}

	// Payment is an explicit choice, never a default. A mint that quietly
	// issued to anyone would be indistinguishable from a working one right up
	// until the bill arrived.
	if !*open {
		return errors.New("eregion: no payment rail is configured. " +
			"Implement mint.Authorizer for whatever you sell through, or pass -open to issue free tokens for a demo")
	}
	log.Warn("issuing tokens to anyone who asks; there is no payment behind this mint")

	m, err := mint.New(priv, mint.OpenAuthorizer{})
	if err != nil {
		return err
	}

	if *publish != "" {
		if err := mint.WritePublicKey(&priv.PublicKey, *publish); err != nil {
			return err
		}
		log.Info("published the verification key", "path", *publish)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mint.NewServer(m, log).Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	fmt.Fprintf(os.Stderr, "\n  Mint key id: %s\n\n"+
		"  Gateway operators need the public key. Clients need the key id above,\n"+
		"  and must get it from somewhere other than this server: a mint that\n"+
		"  handed each buyer a key of their own would put every one of them in an\n"+
		"  anonymity set of one.\n\n", m.KeyID())
	log.Info("eregion listening", "addr", *addr, "key", m.KeyID())

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		log.Info("shutting down", "signal", sig.String(), "issued", m.Issued())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
