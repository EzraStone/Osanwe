// Package checkout exposes a small, identity-free storefront for token invoices.
package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/EzraStone/osanwe/internal/btcpay"
)

const maxRequestBody = 1 << 10

// InvoiceCreator is the only payment capability the public checkout needs.
type InvoiceCreator interface {
	CreateInvoice(context.Context, string, string) (*btcpay.Invoice, error)
}

// Config defines one fixed-price token product.
type Config struct {
	Creator  InvoiceCreator
	Amount   string
	Currency string

	// MaxInvoicesPerMinute is a global, identity-free abuse ceiling. It limits
	// invoice creation without retaining buyer IP addresses or browser IDs.
	MaxInvoicesPerMinute int
	Logger               *slog.Logger
}

// Server serves the checkout page and its same-origin invoice endpoint.
type Server struct {
	creator  InvoiceCreator
	amount   string
	currency string
	limiter  *minuteLimiter
	log      *slog.Logger
}

// NewServer validates and constructs a fixed-price checkout.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Creator == nil {
		return nil, errors.New("checkout: an invoice creator is required")
	}
	if _, ok := btcpay.ExactPositiveDecimal(cfg.Amount); !ok {
		return nil, errors.New("checkout: amount must be a positive exact decimal")
	}
	currency, ok := btcpay.NormalizeCurrency(cfg.Currency)
	if !ok {
		return nil, errors.New("checkout: currency must contain 2-12 ASCII letters or digits")
	}
	if cfg.MaxInvoicesPerMinute <= 0 {
		return nil, errors.New("checkout: max invoices per minute must be positive")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{
		creator: cfg.Creator, amount: cfg.Amount, currency: currency,
		limiter: newMinuteLimiter(cfg.MaxInvoicesPerMinute), log: cfg.Logger,
	}, nil
}

// Handler returns the checkout's four routes. It deliberately has no cookies,
// accounts, analytics, CORS, or endpoints that accept buyer metadata.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.page)
	mux.HandleFunc("GET /app.css", s.styles)
	mux.HandleFunc("GET /app.js", s.script)
	mux.HandleFunc("POST /api/checkout", s.create)
	return securityHeaders(mux)
}

func (s *Server) page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	page := strings.Replace(checkoutPage, "__AMOUNT__", s.amount, 1)
	page = strings.Replace(page, "__CURRENCY__", s.currency, 1)
	_, _ = io.WriteString(w, page)
}

func (s *Server) styles(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = io.WriteString(w, checkoutCSS)
}

func (s *Server) script(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = io.WriteString(w, checkoutJS)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeError(w, http.StatusForbidden, "cross-origin invoice creation is not allowed")
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request")
		return
	}
	if len(body) > maxRequestBody {
		writeError(w, http.StatusRequestEntityTooLarge, "request is too large")
		return
	}
	var request struct{}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "request must be an empty JSON object")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request contains trailing JSON")
		return
	}
	if !s.limiter.Allow(time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "invoice creation is temporarily busy")
		return
	}

	invoice, err := s.creator.CreateInvoice(r.Context(), s.amount, s.currency)
	if err != nil {
		s.log.Error("creating checkout invoice", "error", err)
		writeError(w, http.StatusBadGateway, "could not create a payment invoice")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		InvoiceID   string `json:"invoice_id"`
		CheckoutURL string `json:"checkout_url"`
		Amount      string `json:"amount"`
		Currency    string `json:"currency"`
	}{invoice.ID, invoice.CheckoutLink, s.amount, s.currency})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

type minuteLimiter struct {
	mu      sync.Mutex
	start   time.Time
	used    int
	maximum int
}

func newMinuteLimiter(maximum int) *minuteLimiter {
	return &minuteLimiter{maximum: maximum}
}

func (l *minuteLimiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.start.IsZero() || now.Sub(l.start) >= time.Minute || now.Before(l.start) {
		l.start = now
		l.used = 0
	}
	if l.used >= l.maximum {
		return false
	}
	l.used++
	return true
}

const checkoutPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Osanwë private token</title>
  <link rel="stylesheet" href="/app.css">
</head>
<body>
  <main>
    <p class="eyebrow">OSANWË</p>
    <h1>Buy one private access token</h1>
    <p>Payment authorizes one blind signature. The mint can see the paid invoice, but cannot link it to the token you later spend.</p>
    <section aria-labelledby="price-heading">
      <h2 id="price-heading">Price</h2>
      <p class="price" id="price" data-amount="__AMOUNT__" data-currency="__CURRENCY__"></p>
      <button id="buy" type="button">Create payment invoice</button>
      <p id="status" role="status" aria-live="polite"></p>
    </section>
    <section id="receipt" hidden>
      <h2>Save your receipt</h2>
      <p>After BTCPay reports the invoice settled, use this invoice ID as <code>OSANWE_RECEIPT</code>. It is a one-shot bearer receipt; keep it private until your client redeems it.</p>
      <output id="invoice-id"></output>
      <button id="copy" type="button">Copy invoice ID</button>
      <a id="pay" rel="noreferrer">Continue to BTCPay</a>
    </section>
    <p class="privacy">No account, cookie, analytics, buyer name, or email address is requested by this checkout.</p>
  </main>
  <script src="/app.js" defer></script>
</body>
</html>`

const checkoutCSS = `:root{color-scheme:dark;font:17px/1.55 system-ui,sans-serif;background:#09110f;color:#e6f1ec}body{margin:0}main{max-width:42rem;margin:8vh auto;padding:2rem}.eyebrow{letter-spacing:.24em;color:#7bd7ab}h1{font-size:clamp(2rem,7vw,4.5rem);line-height:1;margin:.2em 0}.price{font-size:2rem;font-weight:700}section{border:1px solid #29483e;border-radius:1rem;padding:1.5rem;margin:2rem 0;background:#0e1a17}button,a{display:inline-block;border:0;border-radius:.5rem;padding:.8rem 1rem;background:#7bd7ab;color:#07100d;font:inherit;font-weight:700;cursor:pointer;text-decoration:none}button:disabled{opacity:.6;cursor:wait}#status{min-height:1.5em;color:#e9bd76}#invoice-id{display:block;overflow-wrap:anywhere;padding:1rem;margin:1rem 0;background:#07100d;border-radius:.4rem;font-family:monospace}#pay{margin-left:.5rem}.privacy{color:#a8bbb3;font-size:.9rem}code{color:#a7e5c8}@media(max-width:36rem){main{margin:0;padding:1.25rem}#pay{display:block;margin:.75rem 0 0;text-align:center}}`

const checkoutJS = `(() => {
  const buy = document.getElementById('buy');
  const status = document.getElementById('status');
  const receipt = document.getElementById('receipt');
  const invoiceID = document.getElementById('invoice-id');
  const pay = document.getElementById('pay');
  const price = document.getElementById('price');
  price.textContent = price.dataset.amount + ' ' + price.dataset.currency;
  buy.addEventListener('click', async () => {
    buy.disabled = true;
    status.textContent = 'Creating a private payment invoice…';
    try {
      const response = await fetch('/api/checkout', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'});
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || 'Invoice creation failed');
      invoiceID.textContent = result.invoice_id;
      pay.href = result.checkout_url;
      receipt.hidden = false;
      status.textContent = 'Invoice created. Save the receipt, then pay with BTCPay.';
    } catch (error) {
      status.textContent = error.message;
      buy.disabled = false;
    }
  });
  document.getElementById('copy').addEventListener('click', async () => {
    await navigator.clipboard.writeText(invoiceID.textContent);
    status.textContent = 'Invoice ID copied.';
  });
})();`
