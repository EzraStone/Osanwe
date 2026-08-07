package mint

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// ErrAlreadySpent is returned when a token is presented a second time.
var ErrAlreadySpent = errors.New("mint: token has already been spent")

// ErrNotPaid is returned when an issuance request has no valid entitlement
// behind it.
var ErrNotPaid = errors.New("mint: no paid entitlement for this request")

// Authorizer decides whether one token may be issued.
//
// This is the seam where payment lives, and it is deliberately narrow: the mint
// asks "has this been paid for", gets yes or no, and never learns anything
// else. Card, bank transfer, cash in an envelope and Monero all reduce to the
// same one-bit answer, which is what lets the payment rail change without the
// crypto changing underneath it.
//
// The receipt must not identify a person to the caller of Issue. Whatever
// identity the payment carried belongs at the point of sale; carrying it
// through to issuance would put the buyer's name next to a blinded message and
// undo the entire construction.
type Authorizer interface {
	Authorize(ctx context.Context, receipt []byte) error
}

// OpenAuthorizer issues to anyone who asks. It exists for tests and local
// development, and it is a named type rather than a nil check so that running
// a mint without payment is a visible decision in the code that configures it.
type OpenAuthorizer struct{}

func (OpenAuthorizer) Authorize(context.Context, []byte) error { return nil }

// Mint holds the signing key and issues tokens against paid entitlements.
type Mint struct {
	priv  *rsa.PrivateKey
	keyID string
	auth  Authorizer

	issued sync.Map // not a counter of who, only of how many
	count  struct {
		mu sync.Mutex
		n  uint64
	}
}

// New returns a Mint. A nil Authorizer is refused: an accidental open mint
// prints money.
func New(priv *rsa.PrivateKey, auth Authorizer) (*Mint, error) {
	if priv == nil {
		return nil, errors.New("mint: a signing key is required")
	}
	if priv.N.BitLen() < MinKeyBits {
		return nil, fmt.Errorf("mint: key is %d bits, refusing anything under %d", priv.N.BitLen(), MinKeyBits)
	}
	if auth == nil {
		return nil, errors.New("mint: an Authorizer is required; pass OpenAuthorizer{} to deliberately issue to anyone")
	}
	if err := priv.Validate(); err != nil {
		return nil, fmt.Errorf("mint: signing key is not valid: %w", err)
	}
	return &Mint{priv: priv, keyID: KeyID(&priv.PublicKey), auth: auth}, nil
}

// PublicKey returns the key clients blind against and gateways verify with.
func (m *Mint) PublicKey() *rsa.PublicKey { return &m.priv.PublicKey }

// KeyID names the current key.
func (m *Mint) KeyID() string { return m.keyID }

// Issued reports how many tokens have been signed. It is a total and nothing
// else: a mint that recorded who each issuance was for would hold exactly the
// record this design exists to avoid creating.
func (m *Mint) Issued() uint64 {
	m.count.mu.Lock()
	defer m.count.mu.Unlock()
	return m.count.n
}

// Issue signs one blinded message, if the receipt entitles the caller to it.
//
// The mint cannot read what it signs, cannot tell two requests apart, and
// cannot recognise the result when it comes back to be spent.
func (m *Mint) Issue(ctx context.Context, receipt, blinded []byte) ([]byte, error) {
	if err := m.auth.Authorize(ctx, receipt); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPaid, err)
	}
	sig, err := SignBlinded(m.priv, blinded)
	if err != nil {
		return nil, err
	}
	m.count.mu.Lock()
	m.count.n++
	m.count.mu.Unlock()
	return sig, nil
}

// SpentSet records tokens that have been redeemed, so one cannot be spent
// twice.
//
// This is where the design's least comfortable trade sits. Double-spend
// prevention needs every redemption checked against every earlier one, so the
// state is shared and cannot be sharded by user -- there is no user to shard
// by. A production gateway needs this in a store several machines can see, and
// that store is the one component that watches every redemption happen. It
// learns nothing about who, but it does learn how many and when.
//
// Entries expire with the key that signed them: rotating a mint key retires
// every token under it, which is what keeps this set from growing without
// bound.
type SpentSet struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	clock func() time.Time
}

// NewSpentSet returns an empty set.
func NewSpentSet() *SpentSet {
	return &SpentSet{seen: make(map[string]time.Time), clock: time.Now}
}

// Spend records a token, reporting ErrAlreadySpent if it has been seen.
//
// Verification is the caller's job and must happen first. Recording an
// unverified token would let anyone fill the set with nonces that were never
// signed, which is a cheap way to deny service to whoever holds the real ones.
func (s *SpentSet) Spend(tok *Token) error {
	if tok == nil {
		return errors.New("mint: nil token")
	}
	key := tok.KeyID + "|" + string(tok.Nonce)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[key]; ok {
		return ErrAlreadySpent
	}
	s.seen[key] = s.clock()
	return nil
}

// Len reports how many tokens have been redeemed.
func (s *SpentSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// Retire drops every token issued under a key, which is what makes rotation
// the garbage collector for this set.
func (s *SpentSet) Retire(keyID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k := range s.seen {
		if len(k) > len(keyID) && k[:len(keyID)] == keyID && k[len(keyID)] == '|' {
			delete(s.seen, k)
			n++
		}
	}
	return n
}

// --------------------------------------------------------------------------
// key storage
// --------------------------------------------------------------------------

// GenerateKey creates a mint signing key.
func GenerateKey(bits int) (*rsa.PrivateKey, error) {
	if bits < MinKeyBits {
		return nil, fmt.Errorf("mint: refusing to generate a %d-bit key; the minimum is %d", bits, MinKeyBits)
	}
	return rsa.GenerateKey(rand.Reader, bits)
}

// WriteKey saves a signing key, refusing to overwrite one that exists.
//
// Overwriting silently would invalidate every token already issued under the
// old key, and the failure would appear as customers being told their paid
// tokens are counterfeit.
func WriteKey(priv *rsa.PrivateKey, path string) error {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("mint: encoding key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("mint: %s already exists; refusing to overwrite a signing key, since every token issued under it would become unverifiable", path)
		}
		return fmt.Errorf("mint: writing key: %w", err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// LoadKey reads a signing key.
func LoadKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mint: reading key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("mint: %s contains no PEM block", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mint: parsing key: %w", err)
	}
	priv, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("mint: %s holds a %T, not an RSA key", path, parsed)
	}
	if priv.N.BitLen() < MinKeyBits {
		return nil, fmt.Errorf("mint: %s holds a %d-bit key, below the %d minimum", path, priv.N.BitLen(), MinKeyBits)
	}
	return priv, nil
}

// LoadOrCreateKey loads a key, generating one if the file is absent. The bool
// reports whether a key was created.
func LoadOrCreateKey(path string, bits int) (*rsa.PrivateKey, bool, error) {
	priv, err := LoadKey(path)
	if err == nil {
		return priv, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	priv, err = GenerateKey(bits)
	if err != nil {
		return nil, false, err
	}
	if err := WriteKey(priv, path); err != nil {
		return nil, false, err
	}
	return priv, true, nil
}
