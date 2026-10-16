package mint

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/EzraStone/osanwe/internal/btcpay"
)

// BTCPayConfig configures settled-invoice authorization against a self-hosted
// BTCPay Server Greenfield API.
type BTCPayConfig struct {
	Endpoint string
	StoreID  string
	APIKey   string

	// Amount and Currency are the exact price of one token. Checking both keeps
	// any other settled invoice in the store from becoming an entitlement.
	Amount   string
	Currency string

	Receipts ReceiptStore

	HTTPClient *http.Client
}

// BTCPayAuthorizer turns one settled invoice into one blind signature. The
// invoice ID is a bearer receipt and is durably consumed after verification.
type BTCPayAuthorizer struct {
	client   *btcpay.Client
	storeID  string
	amount   string
	currency string
	receipts ReceiptStore
}

func NewBTCPayAuthorizer(cfg BTCPayConfig) (*BTCPayAuthorizer, error) {
	if cfg.Receipts == nil {
		return nil, errors.New("mint: a durable ReceiptStore is required for BTCPay; otherwise one invoice could issue unlimited tokens")
	}
	client, currency, err := btcpaySettings(cfg)
	if err != nil {
		return nil, err
	}

	return &BTCPayAuthorizer{
		client: client, storeID: cfg.StoreID, amount: cfg.Amount,
		currency: currency, receipts: cfg.Receipts,
	}, nil
}

// CheckBTCPaySettings validates everything in cfg except Receipts, so a
// command can refuse a bad endpoint or price before it creates a signing key
// or a receipt database it would then leave behind.
func CheckBTCPaySettings(cfg BTCPayConfig) error {
	_, _, err := btcpaySettings(cfg)
	return err
}

func btcpaySettings(cfg BTCPayConfig) (*btcpay.Client, string, error) {
	currency, ok := btcpay.NormalizeCurrency(cfg.Currency)
	if !ok {
		return nil, "", errors.New("mint: BTCPay token currency must contain 2-12 ASCII letters or digits")
	}
	if _, ok := btcpay.ExactPositiveDecimal(cfg.Amount); !ok {
		return nil, "", errors.New("mint: BTCPay token amount must be a positive exact decimal")
	}
	client, err := btcpay.New(btcpay.Config{
		Endpoint: cfg.Endpoint, StoreID: cfg.StoreID, APIKey: cfg.APIKey,
		HTTPClient: cfg.HTTPClient,
	})
	if err != nil {
		return nil, "", fmt.Errorf("mint: configuring BTCPay: %w", err)
	}
	return client, currency, nil
}

func (a *BTCPayAuthorizer) Authorize(ctx context.Context, receipt []byte) error {
	invoiceID := string(receipt)
	if !btcpay.SafeIdentifier(invoiceID, 256) {
		return errors.New("BTCPay invoice receipt is empty or malformed")
	}
	invoice, err := a.client.GetInvoice(ctx, invoiceID)
	if err != nil {
		return fmt.Errorf("checking BTCPay invoice: %w", err)
	}
	if invoice.Status != "Settled" {
		return fmt.Errorf("BTCPay invoice is %s, not Settled", printableStatus(invoice.Status))
	}
	if invoice.Currency != a.currency {
		return errors.New("BTCPay invoice currency does not match the token price")
	}
	equal, err := btcpay.AmountsEqual(invoice.Amount, a.amount)
	if err != nil || !equal {
		return errors.New("BTCPay invoice amount does not match the token price")
	}

	if err := a.receipts.Claim(ctx, "btcpay:"+a.storeID, receipt); err != nil {
		return err
	}
	return nil
}

func printableStatus(status string) string {
	if status == "" || len(status) > 32 {
		return "not settled"
	}
	return status
}

var _ Authorizer = (*BTCPayAuthorizer)(nil)
