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
// A production mint verifies one settled BTCPay Server invoice per token. An
// explicitly enabled free-beta mode consumes one offline-generated voucher per
// token. -open remains for demos and is incompatible with both modes.
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

type authorizationMode uint8

const (
	authorizationModeNone authorizationMode = iota
	authorizationModeOpen
	authorizationModeBTCPay
	authorizationModeInvite
)

func selectAuthorizationMode(open bool, btcpayEndpoint, inviteManifest string) (authorizationMode, error) {
	modes := 0
	if open {
		modes++
	}
	if btcpayEndpoint != "" {
		modes++
	}
	if inviteManifest != "" {
		modes++
	}
	if modes > 1 {
		return authorizationModeNone, errors.New("eregion: -open, -btcpay, and -invite-manifest are mutually exclusive authorization modes")
	}
	switch {
	case open:
		return authorizationModeOpen, nil
	case btcpayEndpoint != "":
		return authorizationModeBTCPay, nil
	case inviteManifest != "":
		return authorizationModeInvite, nil
	default:
		return authorizationModeNone, nil
	}
}

func validateInviteCapacityFlag(mode authorizationMode, capacity int) error {
	if mode == authorizationModeInvite && capacity < 1 {
		return errors.New("eregion: -invite-capacity is required and must be positive in invite mode; pin the expected manifest total explicitly")
	}
	if mode != authorizationModeInvite && capacity != 0 {
		return errors.New("eregion: -invite-capacity is valid only with -invite-manifest")
	}
	return nil
}

func validateLoadedInviteCapacity(expected, actual int) error {
	if actual != expected {
		return fmt.Errorf("eregion: invite manifest capacity is %d, expected exactly %d; refusing an unexpected entitlement total", actual, expected)
	}
	return nil
}

func run() error {
	fs := flag.NewFlagSet("eregion", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8445", "address to listen on")
	keyPath := fs.String("key", "mint.key", "signing key path, created if absent")
	publish := fs.String("publish", "", "also write the public key to this path, for gateway operators")
	bits := fs.Int("bits", mint.MinKeyBits, "key size when generating")
	open := fs.Bool("open", false, "issue a token to anyone who asks, with no payment. For demos only: an open mint prints money")
	btcpayEndpoint := fs.String("btcpay", "", "base URL of a self-hosted BTCPay Server Greenfield API")
	btcpayStore := fs.String("btcpay-store", "", "BTCPay store ID whose settled invoices buy tokens")
	btcpayAmount := fs.String("btcpay-amount", "", "exact BTCPay invoice amount that buys one token")
	btcpayCurrency := fs.String("btcpay-currency", "", "BTCPay invoice currency that buys one token, such as USD")
	inviteManifest := fs.String("invite-manifest", "", "fixed-window free-beta voucher manifest generated offline by invitebook")
	inviteCapacity := fs.Int("invite-capacity", 0, "expected total vouchers in -invite-manifest; required in invite mode")
	receiptsDB := fs.String("receipts-db", "", "durable database of consumed payment or invite entitlements")
	printKey := fs.Bool("print-key-id", false, "print the key id and exit")
	verbose := fs.Bool("v", false, "verbose logging")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "eregion — the Osanwë mint.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n"+
			"  export OSANWE_BTCPAY_API_KEY=...\n"+
			"  eregion -key mint.key -publish mint.pub -btcpay https://pay.example -btcpay-store STORE -btcpay-amount 1.00 -btcpay-currency USD -receipts-db receipts.db\n\n"+
			"For a fixed free beta:\n  eregion -key mint.key -publish mint.pub -invite-manifest invite-manifest.json -invite-capacity 100 -receipts-db receipts.db\n\n"+
			"For a local demo only:\n  eregion -key mint.key -publish mint.pub -open\n\nFlags:\n")
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
	// until the provider bill arrived.
	var authorizer mint.Authorizer
	var receipts *mint.FileReceiptStore
	mode, err := selectAuthorizationMode(*open, *btcpayEndpoint, *inviteManifest)
	if err != nil {
		return err
	}
	if err := validateInviteCapacityFlag(mode, *inviteCapacity); err != nil {
		return err
	}
	switch mode {
	case authorizationModeOpen:
		log.Warn("issuing tokens to anyone who asks; there is no payment behind this mint")
		authorizer = mint.OpenAuthorizer{}
	case authorizationModeBTCPay:
		apiKey := os.Getenv("OSANWE_BTCPAY_API_KEY")
		if apiKey == "" {
			return errors.New("eregion: OSANWE_BTCPAY_API_KEY is required in BTCPay mode")
		}
		if *receiptsDB == "" {
			return errors.New("eregion: -receipts-db is required in BTCPay mode; invoice entitlements must remain one-shot across restarts")
		}
		var err error
		receipts, err = mint.OpenFileReceiptStore(*receiptsDB)
		if err != nil {
			return err
		}
		defer func() {
			if err := receipts.Close(); err != nil {
				log.Error("closing used-receipt database", "error", err)
			}
		}()
		authorizer, err = mint.NewBTCPayAuthorizer(mint.BTCPayConfig{
			Endpoint: *btcpayEndpoint, StoreID: *btcpayStore, APIKey: apiKey,
			Amount: *btcpayAmount, Currency: *btcpayCurrency, Receipts: receipts,
		})
		if err != nil {
			return err
		}
		log.Info("authorizing one token per settled BTCPay invoice",
			"endpoint", *btcpayEndpoint, "store", *btcpayStore,
			"amount", *btcpayAmount, "currency", *btcpayCurrency,
			"receipts_db", *receiptsDB)
	case authorizationModeInvite:
		if *receiptsDB == "" {
			return errors.New("eregion: -receipts-db is required in invite mode; vouchers must remain one-shot across restarts")
		}
		var err error
		receipts, err = mint.OpenFileReceiptStore(*receiptsDB)
		if err != nil {
			return err
		}
		defer func() {
			if err := receipts.Close(); err != nil {
				log.Error("closing used-receipt database", "error", err)
			}
		}()
		invites, err := mint.NewInviteAuthorizer(mint.InviteAuthorizerConfig{
			ManifestPath: *inviteManifest,
			MintKeyID:    mint.KeyID(&priv.PublicKey),
			Receipts:     receipts,
		})
		if err != nil {
			return err
		}
		if err := validateLoadedInviteCapacity(*inviteCapacity, invites.Capacity()); err != nil {
			return err
		}
		authorizer = invites
		start, end := invites.Window()
		log.Info("authorizing fixed-window free-beta invite vouchers",
			"program", invites.ProgramID(), "capacity", invites.Capacity(),
			"not_before", start.Format(time.RFC3339), "not_after", end.Format(time.RFC3339),
			"manifest", *inviteManifest, "receipts_db", *receiptsDB)
	default:
		return errors.New("eregion: no authorization mode is configured; pass -btcpay for payments, -invite-manifest for a fixed free beta, or -open only for a local demo")
	}

	m, err := mint.New(priv, authorizer)
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
