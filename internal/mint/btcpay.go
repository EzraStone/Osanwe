package mint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const btcpayMaxResponse = 64 << 10

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
	base           *url.URL
	storeID        string
	apiKey         string
	currency       string
	expectedAmount *big.Rat
	receipts       ReceiptStore
	client         *http.Client
}

func NewBTCPayAuthorizer(cfg BTCPayConfig) (*BTCPayAuthorizer, error) {
	base, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("mint: parsing BTCPay endpoint: %w", err)
	}
	if base.Host == "" || (base.Scheme != "https" && !(base.Scheme == "http" && loopbackHost(base.Hostname()))) {
		return nil, errors.New("mint: BTCPay endpoint must use HTTPS (plain HTTP is accepted only on loopback for local development)")
	}
	if base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("mint: BTCPay endpoint must not contain user information, a query, or a fragment")
	}
	if !safeIdentifier(cfg.StoreID, 200) {
		return nil, errors.New("mint: BTCPay store ID is required and contains unsupported characters")
	}
	if cfg.APIKey == "" || strings.TrimSpace(cfg.APIKey) != cfg.APIKey || !safeHeaderFragment(cfg.APIKey) {
		return nil, errors.New("mint: BTCPay API key is required and must be safe for an HTTP header")
	}
	if cfg.Receipts == nil {
		return nil, errors.New("mint: a durable ReceiptStore is required for BTCPay; otherwise one invoice could issue unlimited tokens")
	}
	currency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if currency == "" || len(currency) > 12 {
		return nil, errors.New("mint: BTCPay token currency is required")
	}
	amount, ok := new(big.Rat).SetString(strings.TrimSpace(cfg.Amount))
	if !ok || amount.Sign() <= 0 {
		return nil, errors.New("mint: BTCPay token amount must be a positive exact decimal")
	}

	client := http.Client{Timeout: 15 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
		if client.Timeout <= 0 {
			client.Timeout = 15 * time.Second
		}
	}
	// Never allow a store API key to follow a redirect to another endpoint.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &BTCPayAuthorizer{
		base: base, storeID: cfg.StoreID, apiKey: cfg.APIKey,
		currency: currency, expectedAmount: amount, receipts: cfg.Receipts,
		client: &client,
	}, nil
}

func (a *BTCPayAuthorizer) Authorize(ctx context.Context, receipt []byte) error {
	invoiceID := string(receipt)
	if !safeIdentifier(invoiceID, 256) {
		return errors.New("BTCPay invoice receipt is empty or malformed")
	}
	endpoint, err := url.JoinPath(a.base.String(), "api", "v1", "stores", a.storeID, "invoices", invoiceID)
	if err != nil {
		return fmt.Errorf("building BTCPay invoice URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building BTCPay invoice request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+a.apiKey)
	req.Header.Set("User-Agent", "osanwe-eregion")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("checking BTCPay invoice: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, btcpayMaxResponse+1))
	if err != nil {
		return fmt.Errorf("reading BTCPay invoice: %w", err)
	}
	if len(body) > btcpayMaxResponse {
		return errors.New("BTCPay invoice response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("BTCPay invoice lookup returned HTTP %d", resp.StatusCode)
	}

	var invoice struct {
		ID       string          `json:"id"`
		StoreID  string          `json:"storeId"`
		Amount   json.RawMessage `json:"amount"`
		Currency string          `json:"currency"`
		Status   string          `json:"status"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&invoice); err != nil {
		return fmt.Errorf("decoding BTCPay invoice: %w", err)
	}
	if invoice.ID != invoiceID {
		return errors.New("BTCPay returned a different invoice than the one requested")
	}
	if invoice.StoreID != "" && invoice.StoreID != a.storeID {
		return errors.New("BTCPay returned an invoice from a different store")
	}
	if invoice.Status != "Settled" {
		return fmt.Errorf("BTCPay invoice is %s, not Settled", printableStatus(invoice.Status))
	}
	if strings.ToUpper(invoice.Currency) != a.currency {
		return errors.New("BTCPay invoice currency does not match the token price")
	}
	amount, err := decodeBTCPayAmount(invoice.Amount)
	if err != nil || amount.Cmp(a.expectedAmount) != 0 {
		return errors.New("BTCPay invoice amount does not match the token price")
	}

	if err := a.receipts.Claim(ctx, "btcpay:"+a.storeID, receipt); err != nil {
		return err
	}
	return nil
}

func decodeBTCPayAmount(raw json.RawMessage) (*big.Rat, error) {
	value := strings.TrimSpace(string(raw))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		value = text
	}
	amount, ok := new(big.Rat).SetString(value)
	if !ok || amount.Sign() <= 0 {
		return nil, errors.New("invalid amount")
	}
	return amount, nil
}

func safeIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func safeHeaderFragment(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

func loopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func printableStatus(status string) string {
	if status == "" || len(status) > 32 {
		return "not settled"
	}
	return status
}

var _ Authorizer = (*BTCPayAuthorizer)(nil)
