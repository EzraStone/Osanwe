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

	count struct {
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
	if err := validatePrivateKey(priv); err != nil {
		return nil, fmt.Errorf("mint: signing key is not valid: %w", err)
	}
	if auth == nil {
		return nil, errors.New("mint: an Authorizer is required; pass OpenAuthorizer{} to deliberately issue to anyone")
	}
	return &Mint{priv: priv, keyID: KeyID(&priv.PublicKey), auth: auth}, nil
}

// PublicKey returns the key clients blind against and gateways verify with.
// The modulus is copied because big.Int is mutable; callers must not be able
// to corrupt the mint's validated signing key through the public-key view.
func (m *Mint) PublicKey() *rsa.PublicKey { return clonePublicKey(&m.priv.PublicKey) }

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
	// Reject malformed protocol input before asking the payment rail to consume
	// an entitlement. Authorization is allowed to be one-shot; an attacker must
	// not be able to burn a valid receipt with a value the signer cannot accept.
	if err := ValidateBlinded(&m.priv.PublicKey, blinded); err != nil {
		return nil, err
	}
	if err := m.auth.Authorize(ctx, receipt); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPaid, err)
	}
	sig, err := signValidatedBlinded(m.priv, blinded)
	if err != nil {
		return nil, err
	}
	m.count.mu.Lock()
	m.count.n++
	m.count.mu.Unlock()
	return sig, nil
}

// RedemptionStore atomically records and releases token redemptions.
//
// Spend must not return nil until the redemption is durable. A second Spend
// for the same token must return ErrAlreadySpent, including when the calls
// race or arrive through different users of a shared implementation. Refund
// must not make a token spendable until that change is durable. Callers must
// fail closed on every other error from either method.
//
// Implementations shared by several gateways are the synchronization boundary
// for double-spend prevention. They learn no client identity or prompt, but do
// observe the opaque token fingerprints and timing of every redemption.
type RedemptionStore interface {
	Spend(tok *Token) error
	Refund(tok *Token) error
}

// SpentSet records tokens that have been redeemed, so one cannot be spent
// twice. It is process-local and intended for tests; OpenFileSpentSet is the
// durable implementation used by mithlond.
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

// Refund un-spends a token.
//
// A token has to be marked spent before the request it paid for is forwarded,
// because anything else leaves a window where the same token buys two
// requests at once. That ordering means a provider outage would otherwise
// silently consume what somebody paid for. Refunding restores it, but only
// when the request produced nothing: once a response has started, the token is
// gone whatever happens next.
//
// This cannot be used to get free retries. A refund only happens where there
// was no output to keep.
func (s *SpentSet) Refund(tok *Token) error {
	if tok == nil {
		return errors.New("mint: nil token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, tok.KeyID+"|"+string(tok.Nonce))
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
	if err := validatePrivateKey(priv); err != nil {
		return fmt.Errorf("mint: refusing to write an invalid signing key: %w", err)
	}
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
	return pem.Encode(f, &pem.Block{Type: privateKeyPEMType, Bytes: der})
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
	if block.Type != privateKeyPEMType {
		return nil, fmt.Errorf("mint: %s is a legacy or non-Osanwe RSA key (%q); RFC 9474 requires a dedicated key, so generate a new mint key instead of reusing it", path, block.Type)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mint: parsing key: %w", err)
	}
	priv, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("mint: %s holds a %T, not an RSA key", path, parsed)
	}
	if err := validatePrivateKey(priv); err != nil {
		return nil, fmt.Errorf("mint: %s holds an invalid RSA key: %w", path, err)
	}
	return priv, nil
}

// WritePublicKey saves the verification half of a mint key.
//
// This is the file a gateway operator needs and a mint operator publishes. It
// is not secret, and the whole point is that anyone can hold it: a token is
// only worth something because its signature can be checked by a party the
// mint has no relationship with.
func WritePublicKey(pub *rsa.PublicKey, path string) error {
	if err := validatePublicKey(pub); err != nil {
		return fmt.Errorf("mint: refusing to write an invalid public key: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return fmt.Errorf("mint: encoding public key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("mint: writing public key: %w", err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: publicKeyPEMType, Bytes: der})
}

// LoadPublicKey reads a mint's verification key.
func LoadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mint: reading public key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("mint: %s contains no PEM block", path)
	}
	if block.Type != publicKeyPEMType {
		return nil, fmt.Errorf("mint: %s is a legacy or non-Osanwe mint key (%q); publish a dedicated RFC 9474 key", path, block.Type)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mint: parsing public key: %w", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("mint: %s holds a %T, not an RSA public key", path, parsed)
	}
	if err := validatePublicKey(pub); err != nil {
		return nil, fmt.Errorf("mint: %s holds an invalid RSA public key: %w", path, err)
	}
	return pub, nil
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
