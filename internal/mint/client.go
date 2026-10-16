package mint

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// marshalPublicPEM encodes a verification key for publication.
func marshalPublicPEM(pub *rsa.PublicKey) ([]byte, error) {
	if err := validatePublicKey(pub); err != nil {
		return nil, fmt.Errorf("mint: refusing to marshal an invalid public key: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMType, Bytes: der}), nil
}

func errorIs(err, target error) bool { return errors.Is(err, target) }

// MaxKeyBody bounds a published key document.
const MaxKeyBody = 16 << 10

// Client talks to a mint over HTTP.
type Client struct {
	// URL is the mint's base URL.
	URL string

	// ExpectKeyID is the key id the caller obtained out of band. It is
	// required, and the reason is not pedantry: a mint that handed every buyer
	// a distinct key would deanonymise all of them at redemption while looking
	// like it was working. Verifying the key against a value that arrived by a
	// different route is what makes the anonymity set real.
	ExpectKeyID string

	HTTPClient *http.Client

	mu  sync.Mutex
	pub *rsa.PublicKey
}

// httpClient never follows redirects. A 307 or 308 answer to POST /issue
// would otherwise send the request body, receipt or invite voucher included,
// on to wherever the Location header pointed.
func (c *Client) httpClient() *http.Client {
	client := http.Client{Timeout: 30 * time.Second}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// CheckMintURL reports whether raw is a mint URL a client may send receipts
// to. A receipt or invite voucher is a bearer credential, so it must not
// cross a network in the clear: plain HTTP is accepted only on loopback, as
// for a mint on the same machine.
func CheckMintURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mint: parsing the mint URL: %w", err)
	}
	if u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackHost(u.Hostname()))) {
		return fmt.Errorf("mint: the mint URL %q must use HTTPS; plain HTTP is accepted only on loopback, "+
			"since receipts and invite vouchers sent over it can be read and redeemed by anyone on the path", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("mint: the mint URL %q must not contain user information, a query, or a fragment", raw)
	}
	return nil
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}

func (c *Client) endpoint(path string) (string, error) {
	if err := CheckMintURL(c.URL); err != nil {
		return "", err
	}
	return strings.TrimSuffix(c.URL, "/") + path, nil
}

// Key fetches and verifies the mint's public key. A verified key is cached; a
// failure is not. Caching the first result, error and all, meant a mint that
// was unreachable for the few seconds when a client started stayed unusable
// until the client was restarted.
func (c *Client) Key(ctx context.Context) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pub != nil {
		return c.pub, nil
	}
	pub, err := c.fetchKey(ctx)
	if err != nil {
		return nil, err
	}
	c.pub = pub
	return pub, nil
}

func (c *Client) fetchKey(ctx context.Context) (*rsa.PublicKey, error) {
	if c.ExpectKeyID == "" {
		return nil, errors.New("mint: ExpectKeyID is required; without it a mint could hand you a key nobody else uses and read your redemptions directly")
	}

	endpoint, err := c.endpoint("/key")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("mint: fetching the key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mint: fetching the key: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxKeyBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxKeyBody {
		return nil, errors.New("mint: published key is implausibly large")
	}

	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("mint: published key is not PEM")
	}
	if block.Type != publicKeyPEMType {
		return nil, fmt.Errorf("mint: published key uses legacy or unexpected PEM type %q; refusing a key not dedicated to RFC 9474", block.Type)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mint: parsing the published key: %w", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("mint: published a %T, not an RSA key", parsed)
	}
	if err := validatePublicKey(pub); err != nil {
		return nil, fmt.Errorf("mint: published an invalid RSA key: %w", err)
	}
	if got := KeyID(pub); got != c.ExpectKeyID {
		return nil, fmt.Errorf("mint: published key is %s, expected %s. "+
			"Either the mint rotated and you have a stale id, or something is serving you a key of its own", got, c.ExpectKeyID)
	}
	return pub, nil
}

// Token buys one token, doing the blinding on this side so the mint never sees
// what it signed.
func (c *Client) Token(ctx context.Context, receipt string) (*Token, error) {
	pub, err := c.Key(ctx)
	if err != nil {
		return nil, err
	}

	bl, err := Blind(pub)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(IssueRequest{
		Receipt: receipt,
		Blinded: base64.RawURLEncoding.EncodeToString(bl.Blinded),
	})
	if err != nil {
		return nil, err
	}

	endpoint, err := c.endpoint("/issue")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("mint: requesting a token: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxIssueBody+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusPaymentRequired {
		return nil, ErrNotPaid
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mint: requesting a token: HTTP %d", resp.StatusCode)
	}

	var out IssueResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mint: parsing the mint's response: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(out.Signature)
	if err != nil {
		return nil, fmt.Errorf("mint: decoding the signature: %w", err)
	}

	// Unblind verifies before returning, so a mint that answered with rubbish
	// is caught here rather than at spend time.
	return bl.Unblind(sig)
}

// Wallet keeps unspent tokens on hand.
//
// One token buys one request, so without a buffer every request would wait for
// a round trip to the mint before it could start. Refilling ahead of time also
// separates issuance from use in time, which matters: a mint and a gateway
// that both kept timestamps could otherwise correlate "issued at 10:04:03" with
// "spent at 10:04:03" without needing to break anything.
type Wallet struct {
	client  *Client
	receipt string
	// receiptPresented makes non-empty payment receipts one-shot. BTCPay
	// invoices authorize one issuance, not a refillable balance.
	receiptPresented bool

	// LowWater is how few tokens may remain before a refill starts.
	lowWater int
	batch    int

	mu     sync.Mutex
	tokens []*Token
	refill chan struct{}
	spent  uint64
}

// NewWallet returns a Wallet drawing on the given mint.
func NewWallet(c *Client, receipt string, batch int) *Wallet {
	if batch < 1 {
		batch = 8
	}
	return &Wallet{
		client:   c,
		receipt:  receipt,
		lowWater: batch / 2,
		batch:    batch,
		refill:   make(chan struct{}, 1),
	}
}

// Take returns an unspent token, buying more if the wallet is running low.
func (w *Wallet) Take(ctx context.Context) (*Token, error) {
	w.mu.Lock()
	if n := len(w.tokens); n > 0 {
		tok := w.tokens[n-1]
		w.tokens = w.tokens[:n-1]
		w.spent++
		low := len(w.tokens) <= w.lowWater
		w.mu.Unlock()
		if low {
			w.triggerRefill()
		}
		return tok, nil
	}
	w.mu.Unlock()

	// Empty: buy one synchronously rather than failing the request.
	tok, err := w.buy(ctx)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.spent++
	w.mu.Unlock()
	w.triggerRefill()
	return tok, nil
}

// Put returns an unused token to the wallet, so a request that failed before
// the token was spent does not throw away something already paid for.
//
// This is distinct from add below, and the difference is not cosmetic. Put
// reverses a Take, so it un-counts the spend; add merely stocks the wallet
// with something newly bought. Using one for the other makes every refill
// quietly cancel a spend, which is how the counter first came to read zero
// after a request that had plainly gone through.
func (w *Wallet) Put(tok *Token) {
	if tok == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tokens = append(w.tokens, tok)
	if w.spent > 0 {
		w.spent--
	}
}

// add stocks the wallet with a token that has never been handed out.
func (w *Wallet) add(tok *Token) {
	if tok == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tokens = append(w.tokens, tok)
}

// Len reports how many tokens are on hand.
func (w *Wallet) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.tokens)
}

// Spent reports how many tokens have left this wallet.
func (w *Wallet) Spent() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spent
}

func (w *Wallet) triggerRefill() {
	select {
	case w.refill <- struct{}{}:
	default: // a refill is already pending
	}
}

var ErrReceiptAlreadyPresented = errors.New("mint: this payment receipt has already been presented; import a new entitlement")

func (w *Wallet) buy(ctx context.Context) (*Token, error) {
	// The key comes first. Until it is in hand nothing is sent to /issue, so
	// a failure here has not shown the mint the receipt and must not use it.
	if _, err := w.client.Key(ctx); err != nil {
		return nil, err
	}
	w.mu.Lock()
	if w.receipt != "" {
		if w.receiptPresented {
			w.mu.Unlock()
			return nil, ErrReceiptAlreadyPresented
		}
		// Mark it before the network call. If the response is lost after the
		// mint durably claims the invoice, retrying with a different blinded
		// message cannot recover it and would only obscure the ambiguity.
		w.receiptPresented = true
	}
	w.mu.Unlock()
	return w.client.Token(ctx, w.receipt)
}

// Run buys tokens in the background until ctx is cancelled.
//
// It fills once before waiting for anything. A wallet that only stocked itself
// after the first request would make that request pay for a round trip to the
// mint, and would show a balance of zero to anyone who looked before asking
// their first question -- which reads as broken rather than as empty.
func (w *Wallet) Run(ctx context.Context) {
	w.fill(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.refill:
			w.fill(ctx)
		}
	}
}

func (w *Wallet) fill(ctx context.Context) {
	for i := 0; i < w.batch; i++ {
		w.mu.Lock()
		enough := len(w.tokens) >= w.batch
		w.mu.Unlock()
		if enough || ctx.Err() != nil {
			return
		}
		tok, err := w.buy(ctx)
		if err != nil {
			// Refilling is best effort. Take falls back to buying one
			// synchronously, so a mint being briefly unreachable slows
			// requests down rather than failing them.
			return
		}
		w.add(tok)
	}
}
