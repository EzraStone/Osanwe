// Package btcpay implements the narrow Greenfield API surface Osanwe needs.
package btcpay

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

const maxResponse = 64 << 10

type Config struct {
	Endpoint   string
	StoreID    string
	APIKey     string
	HTTPClient *http.Client
}

type Client struct {
	base    *url.URL
	storeID string
	apiKey  string
	client  *http.Client
}

type Invoice struct {
	ID           string
	StoreID      string
	Amount       string
	Currency     string
	Status       string
	CheckoutLink string
}

func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("btcpay: parsing endpoint: %w", err)
	}
	if base.Host == "" || (base.Scheme != "https" && !(base.Scheme == "http" && loopbackHost(base.Hostname()))) {
		return nil, errors.New("btcpay: endpoint must use HTTPS (plain HTTP is accepted only on loopback for local development)")
	}
	if base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("btcpay: endpoint must not contain user information, a query, or a fragment")
	}
	if !SafeIdentifier(cfg.StoreID, 200) {
		return nil, errors.New("btcpay: store ID is required and contains unsupported characters")
	}
	if cfg.APIKey == "" || strings.TrimSpace(cfg.APIKey) != cfg.APIKey || !safeHeaderFragment(cfg.APIKey) {
		return nil, errors.New("btcpay: API key is required and must be safe for an HTTP header")
	}

	client := http.Client{Timeout: 15 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
		if client.Timeout <= 0 {
			client.Timeout = 15 * time.Second
		}
	}
	// Store API keys never follow redirects. Even redirects that appear to be
	// same-origin are rejected so DNS and reverse-proxy mistakes fail closed.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &Client{base: base, storeID: cfg.StoreID, apiKey: cfg.APIKey, client: &client}, nil
}

func (c *Client) GetInvoice(ctx context.Context, invoiceID string) (*Invoice, error) {
	if !SafeIdentifier(invoiceID, 256) {
		return nil, errors.New("btcpay: invoice ID is empty or malformed")
	}
	endpoint, err := url.JoinPath(c.base.String(), "api", "v1", "stores", c.storeID, "invoices", invoiceID)
	if err != nil {
		return nil, fmt.Errorf("btcpay: building invoice URL: %w", err)
	}
	invoice, err := c.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if invoice.ID != invoiceID {
		return nil, errors.New("btcpay: server returned a different invoice than the one requested")
	}
	return invoice, nil
}

func (c *Client) CreateInvoice(ctx context.Context, amount, currency string) (*Invoice, error) {
	if _, ok := ExactPositiveDecimal(amount); !ok {
		return nil, errors.New("btcpay: invoice amount must be a positive exact decimal")
	}
	currency, ok := NormalizeCurrency(currency)
	if !ok {
		return nil, errors.New("btcpay: invoice currency must contain 2-12 ASCII letters or digits")
	}
	payload, err := json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{Amount: amount, Currency: currency})
	if err != nil {
		return nil, fmt.Errorf("btcpay: encoding invoice request: %w", err)
	}
	endpoint, err := url.JoinPath(c.base.String(), "api", "v1", "stores", c.storeID, "invoices")
	if err != nil {
		return nil, fmt.Errorf("btcpay: building create-invoice URL: %w", err)
	}
	invoice, err := c.do(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return nil, err
	}
	if invoice.ID == "" || !SafeIdentifier(invoice.ID, 256) {
		return nil, errors.New("btcpay: server returned a malformed invoice ID")
	}
	if equal, err := AmountsEqual(invoice.Amount, amount); err != nil || !equal {
		return nil, errors.New("btcpay: created invoice amount does not match the requested price")
	}
	if invoice.Currency != currency {
		return nil, errors.New("btcpay: created invoice currency does not match the requested price")
	}
	if err := c.validateCheckoutLink(invoice.CheckoutLink, invoice.ID); err != nil {
		return nil, err
	}
	return invoice, nil
}

func (c *Client) do(ctx context.Context, method, endpoint string, body []byte) (*Invoice, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("btcpay: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+c.apiKey)
	req.Header.Set("User-Agent", "osanwe")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("btcpay: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("btcpay: reading response: %w", err)
	}
	if len(raw) > maxResponse {
		return nil, errors.New("btcpay: response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("btcpay: request returned HTTP %d", resp.StatusCode)
	}

	var wire struct {
		ID           string          `json:"id"`
		StoreID      string          `json:"storeId"`
		Amount       json.RawMessage `json:"amount"`
		Currency     string          `json:"currency"`
		Status       string          `json:"status"`
		CheckoutLink string          `json:"checkoutLink"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("btcpay: decoding invoice: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("btcpay: invoice response contains trailing JSON values")
	}
	if wire.StoreID != "" && wire.StoreID != c.storeID {
		return nil, errors.New("btcpay: server returned an invoice from a different store")
	}
	amount, err := decodeAmount(wire.Amount)
	if err != nil {
		return nil, errors.New("btcpay: server returned a malformed invoice amount")
	}
	currency, ok := NormalizeCurrency(wire.Currency)
	if !ok {
		return nil, errors.New("btcpay: server returned a malformed invoice currency")
	}
	return &Invoice{
		ID: wire.ID, StoreID: c.storeID, Amount: amount,
		Currency: currency, Status: wire.Status, CheckoutLink: wire.CheckoutLink,
	}, nil
}

func (c *Client) validateCheckoutLink(value, invoiceID string) error {
	checkout, err := url.Parse(value)
	if err != nil || checkout.Scheme != c.base.Scheme || !strings.EqualFold(checkout.Host, c.base.Host) {
		return errors.New("btcpay: checkout link is not on the configured BTCPay origin")
	}
	if checkout.User != nil || checkout.RawQuery != "" || checkout.ForceQuery || checkout.Fragment != "" {
		return errors.New("btcpay: checkout link contains unexpected URL components")
	}
	if !strings.HasSuffix(strings.TrimSuffix(checkout.Path, "/"), "/i/"+invoiceID) {
		return errors.New("btcpay: checkout link does not name the created invoice")
	}
	return nil
}

func ExactPositiveDecimal(value string) (*big.Rat, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return nil, false
	}
	digits, dots := 0, 0
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dots++
		default:
			return nil, false
		}
	}
	if digits == 0 || dots > 1 {
		return nil, false
	}
	amount, ok := new(big.Rat).SetString(value)
	return amount, ok && amount.Sign() > 0
}

func AmountsEqual(a, b string) (bool, error) {
	left, ok := ExactPositiveDecimal(a)
	if !ok {
		return false, errors.New("left amount is not a positive decimal")
	}
	right, ok := ExactPositiveDecimal(b)
	if !ok {
		return false, errors.New("right amount is not a positive decimal")
	}
	return left.Cmp(right) == 0, nil
}

func NormalizeCurrency(value string) (string, bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) < 2 || len(value) > 12 {
		return "", false
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", false
		}
	}
	return value, true
}

func SafeIdentifier(value string, maximum int) bool {
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

func decodeAmount(raw json.RawMessage) (string, error) {
	value := strings.TrimSpace(string(raw))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
	}
	if _, ok := ExactPositiveDecimal(value); !ok {
		return "", errors.New("invalid amount")
	}
	return value, nil
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
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}
