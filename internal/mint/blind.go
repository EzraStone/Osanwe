// Package mint issues unlinkable, single-use bearer tokens.
//
// Token issuance uses the RSABSSA-SHA384-PSS-Randomized variant from RFC 9474.
// The implementation is provided by Cloudflare CIRCL rather than carrying a
// second, application-specific RSA implementation here. The randomized
// variant is one of the RFC's recommended variants and protects the client
// even when the signer supplied a maliciously generated key.
//
// A mint key is dedicated to this protocol. Its key ID includes the protocol
// suite, and key files written by this package carry an Osanwe-specific PEM
// type so a key previously used by the legacy FDH construction is rejected
// instead of silently being reused across protocols.
package mint

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/cloudflare/circl/blindsign/blindrsa"
)

// MinKeyBits is the smallest modulus the mint will work with.
const MinKeyBits = 2048

const (
	// protocolSuite is part of both the key ID and the signed token message.
	// Changing any RSABSSA parameter requires a new value and therefore a new
	// key/anonymity set.
	protocolSuite = "osanwe-rsabssa-sha384-pss-randomized-v1"

	privateKeyPEMType = "OSANWE RSABSSA PRIVATE KEY"
	publicKeyPEMType  = "OSANWE RSABSSA PUBLIC KEY"

	tokenNonceBytes     = 32
	preparePrefixBytes  = 32 // RFC 9474 PrepareRandomize prefix length.
	keyIDHashPrefixSize = 16
)

var (
	// ErrDegenerate rejects a blinded value that cannot be a useful, honestly
	// blinded RFC 9474 message.
	ErrDegenerate = errors.New("mint: blinded message is degenerate")

	// ErrBadSignature means the token is malformed or was not signed by the
	// key it names.
	ErrBadSignature = errors.New("mint: token signature does not verify")
)

// KeyID names both a public key and the exact blind-signature suite used with
// it. A key must never be shared by different RSABSSA variants or protocols.
func KeyID(pub *rsa.PublicKey) string {
	h := sha256.New()
	h.Write([]byte(protocolSuite))
	h.Write([]byte{0})
	if pub != nil && pub.N != nil {
		h.Write(pub.N.Bytes())
		var exponent [8]byte
		binary.BigEndian.PutUint64(exponent[:], uint64(pub.E))
		h.Write(exponent[:])
	}
	return "mint-" + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:keyIDHashPrefixSize])
}

// Blinding is the client-only state for one issuance. It contains the value
// needed to turn the mint's blind signature into a normal RSA-PSS signature.
// It must never be transmitted or persisted by the mint.
type Blinding struct {
	keyID    string
	prepared []byte
	pub      *rsa.PublicKey
	client   *blindrsa.Client
	state    blindrsa.State

	// Blinded is the fixed-width RFC 9474 blinded message sent to the mint.
	Blinded []byte
}

// Blind starts an issuance using fresh entropy for the token nonce, RFC 9474
// message preparation, the PSS salt, and the RSA blinding factor.
func Blind(pub *rsa.PublicKey) (*Blinding, error) {
	nonce := make([]byte, tokenNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("mint: reading token randomness: %w", err)
	}
	return blindNonce(pub, nonce)
}

// blindNonce is Blind with a caller-supplied nonce for focused tests. RFC
// PrepareRandomize and Blind still add independent fresh randomness.
func blindNonce(pub *rsa.PublicKey, nonce []byte) (*Blinding, error) {
	if err := validatePublicKey(pub); err != nil {
		return nil, err
	}
	// rsa.PublicKey contains a mutable *big.Int. Keep caller mutations from
	// changing the key underneath CIRCL after validation has completed.
	pub = clonePublicKey(pub)
	if len(nonce) != tokenNonceBytes {
		return nil, fmt.Errorf("mint: token nonce is %d bytes, want %d", len(nonce), tokenNonceBytes)
	}

	client, err := blindrsa.NewClient(blindrsa.SHA384PSSRandomized, pub)
	if err != nil {
		return nil, fmt.Errorf("mint: creating RFC 9474 client: %w", err)
	}
	keyID := KeyID(pub)
	prepared, err := client.Prepare(rand.Reader, tokenMessage(keyID, nonce))
	if err != nil {
		return nil, fmt.Errorf("mint: preparing RFC 9474 message: %w", err)
	}
	blinded, state, err := client.Blind(rand.Reader, prepared)
	if err != nil {
		return nil, fmt.Errorf("mint: blinding RFC 9474 message: %w", err)
	}

	return &Blinding{
		keyID:    keyID,
		prepared: prepared,
		pub:      pub,
		client:   &client,
		state:    state,
		Blinded:  blinded,
	}, nil
}

// Unblind finalizes RFC 9474 and verifies the resulting RSA-PSS signature
// before returning a spendable token.
func (bl *Blinding) Unblind(blindSig []byte) (*Token, error) {
	if bl == nil || bl.client == nil {
		return nil, errors.New("mint: blinding has already been used or was never started")
	}

	sig, err := bl.client.Finalize(bl.state, blindSig)
	if err != nil {
		return nil, fmt.Errorf("mint: finalizing RFC 9474 signature: %w", err)
	}
	tok := &Token{
		KeyID: bl.keyID,
		Nonce: append([]byte(nil), bl.prepared...),
		Sig:   sig,
	}
	if err := Verify(bl.pub, tok); err != nil {
		return nil, fmt.Errorf("mint: signature failed verification after finalizing: %w", err)
	}

	// A blinding factor is single-use. Clear all client-only protocol state
	// after a successful finalize so accidental reuse fails loudly.
	bl.client = nil
	bl.prepared = nil
	bl.pub = nil
	bl.state = blindrsa.State{}
	return tok, nil
}

// Token is an RFC 9474 prepared message and its RSA-PSS signature. Nonce keeps
// its original wire/API name for compatibility; it now contains the 32-byte
// randomized preparation prefix followed by the domain-separated token nonce.
type Token struct {
	KeyID string
	Nonce []byte
	Sig   []byte
}

// Encode renders a token for transport as three dot-separated fields.
func (t *Token) Encode() string {
	if t == nil {
		return ""
	}
	return t.KeyID + "." +
		base64.RawURLEncoding.EncodeToString(t.Nonce) + "." +
		base64.RawURLEncoding.EncodeToString(t.Sig)
}

// MaxTokenBytes bounds what a verifier will parse.
const MaxTokenBytes = 4096

// ParseToken reads the wire form and rejects non-canonical base64 or a token
// payload that cannot have been produced by this protocol suite.
func ParseToken(s string) (*Token, error) {
	if len(s) == 0 {
		return nil, errors.New("mint: empty token")
	}
	if len(s) > MaxTokenBytes {
		return nil, fmt.Errorf("mint: token is %d bytes, over the %d limit", len(s), MaxTokenBytes)
	}
	if strings.Count(s, ".") != 2 {
		return nil, errors.New("mint: token must have three dot-separated fields")
	}
	parts := strings.Split(s, ".")
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, errors.New("mint: token has an empty field")
	}
	if !strings.HasPrefix(parts[0], "mint-") {
		return nil, errors.New("mint: token has an invalid key id")
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("mint: decoding nonce: %w", err)
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("mint: decoding signature: %w", err)
	}
	if err := validatePreparedMessage(parts[0], nonce); err != nil {
		return nil, err
	}
	return &Token{KeyID: parts[0], Nonce: nonce, Sig: sig}, nil
}

// Verify checks the token as an RFC 9474 randomized RSA-PSS signature and
// enforces Osanwe's domain-separated token message format.
func Verify(pub *rsa.PublicKey, tok *Token) error {
	if err := validatePublicKey(pub); err != nil || tok == nil {
		return ErrBadSignature
	}
	if subtle.ConstantTimeCompare([]byte(tok.KeyID), []byte(KeyID(pub))) != 1 {
		return fmt.Errorf("%w: token names an unexpected key", ErrBadSignature)
	}
	if err := validatePreparedMessage(tok.KeyID, tok.Nonce); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if len(tok.Sig) != byteLen(pub.N) {
		return ErrBadSignature
	}
	verifier, err := blindrsa.NewVerifier(blindrsa.SHA384PSSRandomized, pub)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if err := verifier.Verify(tok.Nonce, tok.Sig); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return nil
}

// ValidateBlinded rejects malformed protocol input before payment
// authorization is consumed or a private-key operation is attempted.
func ValidateBlinded(pub *rsa.PublicKey, blinded []byte) error {
	if err := validatePublicKey(pub); err != nil {
		return err
	}
	if len(blinded) != byteLen(pub.N) {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrDegenerate, len(blinded), byteLen(pub.N))
	}
	b := new(big.Int).SetBytes(blinded)
	if b.Sign() <= 0 || b.Cmp(pub.N) >= 0 || b.Cmp(big.NewInt(1)) == 0 || b.Cmp(new(big.Int).Sub(pub.N, big.NewInt(1))) == 0 {
		return fmt.Errorf("%w: outside the permitted group", ErrDegenerate)
	}
	if new(big.Int).GCD(nil, nil, b, pub.N).Cmp(big.NewInt(1)) != 0 {
		return fmt.Errorf("%w: not coprime to the modulus", ErrDegenerate)
	}
	return nil
}

// SignBlinded performs the mint half of RFC 9474 using CIRCL's checked,
// internally blinded RSA private operation.
func SignBlinded(priv *rsa.PrivateKey, blinded []byte) ([]byte, error) {
	if err := validatePrivateKey(priv); err != nil {
		return nil, err
	}
	if err := ValidateBlinded(&priv.PublicKey, blinded); err != nil {
		return nil, err
	}
	return signValidatedBlinded(priv, blinded)
}

// signValidatedBlinded is used by Mint after New validated the private key and
// Issue validated the protocol input before consuming authorization.
func signValidatedBlinded(priv *rsa.PrivateKey, blinded []byte) ([]byte, error) {
	sig, err := blindrsa.NewSigner(priv).BlindSign(blinded)
	if err != nil {
		return nil, fmt.Errorf("mint: RFC 9474 blind signing: %w", err)
	}
	return sig, nil
}

func tokenMessage(keyID string, nonce []byte) []byte {
	out := make([]byte, 0, len(protocolSuite)+len(keyID)+len(nonce)+2)
	out = append(out, protocolSuite...)
	out = append(out, 0)
	out = append(out, keyID...)
	out = append(out, 0)
	out = append(out, nonce...)
	return out
}

func validatePreparedMessage(keyID string, prepared []byte) error {
	wantLen := preparePrefixBytes + len(protocolSuite) + 1 + len(keyID) + 1 + tokenNonceBytes
	if len(prepared) != wantLen {
		return fmt.Errorf("mint: prepared token message is %d bytes, want %d", len(prepared), wantLen)
	}
	body := prepared[preparePrefixBytes:]
	prefix := protocolSuite + "\x00" + keyID + "\x00"
	if !strings.HasPrefix(string(body), prefix) {
		return errors.New("mint: prepared token message has the wrong domain")
	}
	return nil
}

func validatePublicKey(pub *rsa.PublicKey) error {
	if pub == nil || pub.N == nil {
		return errors.New("mint: nil public key")
	}
	if pub.N.Sign() <= 0 {
		return errors.New("mint: RSA modulus must be positive")
	}
	if pub.N.Bit(0) == 0 {
		return errors.New("mint: RSA modulus must be odd")
	}
	if pub.N.BitLen() < MinKeyBits {
		return fmt.Errorf("mint: key is %d bits, refusing anything under %d", pub.N.BitLen(), MinKeyBits)
	}
	if pub.E < 3 || pub.E%2 == 0 || uint64(pub.E) > uint64(1<<31-1) {
		return errors.New("mint: invalid RSA public exponent")
	}
	return nil
}

func validatePrivateKey(priv *rsa.PrivateKey) error {
	if priv == nil {
		return errors.New("mint: nil private key")
	}
	if err := validatePublicKey(&priv.PublicKey); err != nil {
		return fmt.Errorf("mint: invalid RSA private key: %w", err)
	}
	if len(priv.Primes) != 2 {
		return errors.New("mint: RFC 9474 signing requires a validated two-prime RSA key")
	}
	if priv.D == nil || priv.D.Sign() <= 0 {
		return errors.New("mint: invalid RSA private exponent")
	}
	for _, prime := range priv.Primes {
		if prime == nil || prime.Sign() <= 0 {
			return errors.New("mint: invalid RSA prime factor")
		}
	}
	if err := priv.Validate(); err != nil {
		return fmt.Errorf("mint: invalid RSA private key: %w", err)
	}
	return nil
}

func clonePublicKey(pub *rsa.PublicKey) *rsa.PublicKey {
	if pub == nil || pub.N == nil {
		return nil
	}
	return &rsa.PublicKey{N: new(big.Int).Set(pub.N), E: pub.E}
}

func byteLen(n *big.Int) int { return (n.BitLen() + 7) / 8 }
