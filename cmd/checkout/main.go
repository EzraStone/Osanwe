// Command checkout serves the buyer-facing, fixed-price BTCPay checkout.
//
// Keep it separate from the mint and give it an API key with only
// btcpay.store.cancreateinvoice for the configured store. The mint uses a
// different key with only btcpay.store.canviewinvoices.
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

	"github.com/EzraStone/osanwe/internal/btcpay"
	"github.com/EzraStone/osanwe/internal/checkout"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("checkout", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8446", "address to listen on; put a TLS reverse proxy in front for public use")
	btcpayEndpoint := fs.String("btcpay", "", "base URL of a self-hosted BTCPay Server")
	btcpayStore := fs.String("btcpay-store", "", "BTCPay store ID in which to create invoices")
	amount := fs.String("amount", "", "fixed amount charged for one token")
	currency := fs.String("currency", "", "invoice currency, such as USD")
	maximum := fs.Int("max-invoices-per-minute", 30, "global invoice-creation ceiling without per-buyer tracking")
	verbose := fs.Bool("v", false, "verbose logging")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "checkout — the identity-free Osanwë token storefront.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  export OSANWE_BTCPAY_CREATE_API_KEY=...")
		fmt.Fprintln(os.Stderr, "  checkout -btcpay https://pay.example -btcpay-store STORE -amount 1.00 -currency USD")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "The API key should have only btcpay.store.cancreateinvoice for STORE.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
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
	apiKey := os.Getenv("OSANWE_BTCPAY_CREATE_API_KEY")
	if apiKey == "" {
		return errors.New("checkout: OSANWE_BTCPAY_CREATE_API_KEY is required")
	}
	client, err := btcpay.New(btcpay.Config{
		Endpoint: *btcpayEndpoint, StoreID: *btcpayStore, APIKey: apiKey,
	})
	if err != nil {
		return err
	}
	app, err := checkout.NewServer(checkout.Config{
		Creator: client, Amount: *amount, Currency: *currency,
		MaxInvoicesPerMinute: *maximum, Logger: log,
	})
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Info("checkout listening", "addr", *addr, "btcpay", *btcpayEndpoint,
		"store", *btcpayStore, "amount", *amount, "currency", *currency,
		"invoice_limit_per_minute", *maximum)

	errCh := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		return err
	case <-signalContext.Done():
		log.Info("shutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}
