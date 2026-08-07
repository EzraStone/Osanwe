// Package mint issues tokens that cannot be traced back to who bought them.
//
// The problem it solves is the one routing cannot touch. A relay hides your
// address, but if you still authenticate to the provider with your own account
// then the provider knows exactly who asked, and hiding the address bought
// nothing. Phase 3 replaces the account with a token, and the token has to be
// unlinkable from the purchase or it is just an account with extra steps.
//
// # The construction
//
// Chaum's blind RSA signature. The client picks a random nonce, multiplies its
// hash by r^e for a random r, and sends that. The mint signs what it is given
// and has no way to read it. The client divides the result by r and is left
// with a signature over the nonce -- a signature the mint produced but has
// never seen.
//
//	client   b = H(m) · r^e mod n        the mint sees this
//	mint     s = b^d mod n               and returns this
//	client   σ = s · r⁻¹ mod n           and this is what gets spent
//
// σ = H(m)^d, a valid signature under the mint's key. The mint's view of an
// issuance is (b, s), and there is a valid r connecting that pair to every
// token in existence, not just the one it produced. That is not a claim about
// how hard the linking is; the information is not there. TestMintCannotLink
// demonstrates it by computing the connecting r for every wrong pairing.
//
// # What this deliberately is not
//
// RFC 9474 specifies RSABSSA, which encodes the message with randomized PSS
// rather than a full-domain hash. PSS is the better modern choice and what a
// production mint should end up on. This is the classical FDH construction
// because it can be implemented in a few dozen readable lines against math/big
// and reviewed by eye, which matters more right now than matching a spec no
// counterparty is checking yet.
//
// The private-key operation is a math/big modular exponentiation, and
// big.Int.Exp is explicitly not constant time. The mitigation is the standard
// one and the same one crypto/rsa applies internally: the signer blinds with
// its own random factor before exponentiating, so the timing of the operation
// is not a function of the key. This is stated here rather than buried because
// hand-rolled RSA is exactly the thing that deserves review before it protects
// anyone's money.
package mint

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
)

// MinKeyBits is the smallest modulus the mint will work with. 2048 is the
// floor for anything expected to still be sound in a few years, and a mint key
// that gets factored retroactively mints unlimited free tokens.
const MinKeyBits = 2048

var (
	// ErrDegenerate rejects a blinded value that carries no blinding.
	ErrDegenerate = errors.New("mint: blinded message is degenerate")

	// ErrBadSignature means the token is not signed by the key claimed.
	ErrBadSignature = errors.New("mint: token signature does not verify")
)

// KeyID names a public key, so a verifier knows which key to check against and
// the mint can rotate without ambiguity.
//
// It is also the anonymity set boundary. Everyone holding a token signed by
// one key is indistinguishable within that group and distinguishable from
// everyone else, so a mint that issued a key per customer would be a mint that
// deanonymised every customer while looking like it did nothing wrong. Keys are
// per epoch, never per buyer.
func KeyID(pub *rsa.PublicKey) string {
	h := sha256.New()
	h.Write(pub.N.Bytes())
	h.Write(big.NewInt(int64(pub.E)).Bytes())
	return "mint-" + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}

// hashToRange maps a nonce into the group, domain-separated by key.
//
// Including the key id is what stops a signature made under one key being
// replayed as a token under another: the message being signed is different, so
// the signature simply does not verify.
//
// The result is one bit shorter than the modulus, which guarantees it lands in
// range without a rejection loop. Losing a single bit out of two thousand is
// not a security-relevant reduction of the hash's domain.
func hashToRange(keyID string, nonce []byte, n *big.Int) *big.Int {
	bits := n.BitLen() - 1
	length := (bits + 7) / 8

	seed := sha256.New()
	seed.Write([]byte("osanwe-mint-fdh-1\x00"))
	seed.Write([]byte(keyID))
	seed.Write([]byte{0})
	seed.Write(nonce)

	out := mgf1(seed.Sum(nil), length)

	// Clear the bits above the target length so the value cannot exceed n.
	if excess := length*8 - bits; excess > 0 {
		out[0] &= byte(0xff >> excess)
	}

	h := new(big.Int).SetBytes(out)
	if h.Sign() == 0 {
		// Astronomically unlikely, but a zero here would produce a signature
		// over zero, which verifies against anything.
		h.SetInt64(1)
	}
	return h
}

// mgf1 is the mask generation function from RFC 8017, over SHA-256.
func mgf1(seed []byte, length int) []byte {
	out := make([]byte, 0, length+sha256.Size)
	var counter [4]byte
	for i := 0; len(out) < length; i++ {
		counter[0] = byte(i >> 24)
		counter[1] = byte(i >> 16)
		counter[2] = byte(i >> 8)
		counter[3] = byte(i)
		h := sha256.New()
		h.Write(seed)
		h.Write(counter[:])
		out = h.Sum(out)
	}
	return out[:length]
}

// Blinding is a token part-way through issuance. It holds the secret that
// links the mint's answer back to a usable token, so it never leaves the
// client and is never transmitted.
type Blinding struct {
	keyID string
	nonce []byte
	r     *big.Int
	rInv  *big.Int
	pub   *rsa.PublicKey

	// Blinded is what gets sent to the mint.
	Blinded []byte
}

// Blind starts an issuance. The nonce is freshly random and is what the token
// will ultimately be a signature over.
func Blind(pub *rsa.PublicKey) (*Blinding, error) {
	if pub == nil || pub.N == nil {
		return nil, errors.New("mint: nil public key")
	}
	if pub.N.BitLen() < MinKeyBits {
		return nil, fmt.Errorf("mint: key is %d bits, refusing anything under %d", pub.N.BitLen(), MinKeyBits)
	}

	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("mint: reading randomness: %w", err)
	}
	return blindNonce(pub, nonce)
}

// blindNonce is Blind with the nonce supplied, so a test can hold it fixed and
// check that two blindings of the same nonce still look unrelated.
func blindNonce(pub *rsa.PublicKey, nonce []byte) (*Blinding, error) {
	keyID := KeyID(pub)
	m := hashToRange(keyID, nonce, pub.N)

	// r must be invertible mod n. For an RSA modulus every value below n is
	// invertible unless it shares a factor, which would mean having factored
	// the key -- but the loop costs nothing and removes the assumption.
	var r, rInv *big.Int
	for i := 0; ; i++ {
		if i > 64 {
			return nil, errors.New("mint: could not find an invertible blinding factor")
		}
		candidate, err := rand.Int(rand.Reader, pub.N)
		if err != nil {
			return nil, fmt.Errorf("mint: reading randomness: %w", err)
		}
		if candidate.Sign() == 0 {
			continue
		}
		inv := new(big.Int).ModInverse(candidate, pub.N)
		if inv == nil {
			continue
		}
		r, rInv = candidate, inv
		break
	}

	// b = m · r^e mod n
	re := new(big.Int).Exp(r, big.NewInt(int64(pub.E)), pub.N)
	b := new(big.Int).Mul(m, re)
	b.Mod(b, pub.N)

	if isDegenerate(b, pub.N) {
		// Not reachable with a random r, but signing a degenerate value would
		// hand out a signature the client did not have to blind.
		return nil, ErrDegenerate
	}

	return &Blinding{
		keyID:   keyID,
		nonce:   nonce,
		r:       r,
		rInv:    rInv,
		pub:     pub,
		Blinded: padTo(b, byteLen(pub.N)),
	}, nil
}

// Unblind turns the mint's answer into a spendable token.
func (bl *Blinding) Unblind(blindSig []byte) (*Token, error) {
	if bl == nil || bl.rInv == nil {
		return nil, errors.New("mint: blinding has already been used or was never started")
	}
	s := new(big.Int).SetBytes(blindSig)
	if s.Sign() == 0 || s.Cmp(bl.pub.N) >= 0 {
		return nil, errors.New("mint: signature from the mint is out of range")
	}

	// σ = s · r⁻¹ mod n
	sig := new(big.Int).Mul(s, bl.rInv)
	sig.Mod(sig, bl.pub.N)

	tok := &Token{
		KeyID: bl.keyID,
		Nonce: append([]byte(nil), bl.nonce...),
		Sig:   padTo(sig, byteLen(bl.pub.N)),
	}

	// Verify before returning. A mint that returned garbage -- by accident or
	// to mark one customer's token as distinguishable -- would otherwise be
	// discovered at spend time, by which point the money is gone and the
	// failure looks like the gateway's fault.
	if err := Verify(bl.pub, tok); err != nil {
		return nil, fmt.Errorf("mint: the mint's signature did not verify after unblinding: %w", err)
	}

	// Burn the blinding. Reusing r across two issuances would make both
	// linkable to each other.
	bl.r, bl.rInv = nil, nil
	return tok, nil
}

// Token is what gets spent. It is a signature over a nonce nobody but its
// holder has ever seen.
type Token struct {
	KeyID string
	Nonce []byte
	Sig   []byte
}

// Verify checks a token against a mint key.
func Verify(pub *rsa.PublicKey, tok *Token) error {
	if pub == nil || tok == nil {
		return ErrBadSignature
	}
	if subtle.ConstantTimeCompare([]byte(tok.KeyID), []byte(KeyID(pub))) != 1 {
		return fmt.Errorf("%w: token names key %s", ErrBadSignature, tok.KeyID)
	}
	sig := new(big.Int).SetBytes(tok.Sig)
	if sig.Sign() == 0 || sig.Cmp(pub.N) >= 0 {
		return ErrBadSignature
	}

	// σ^e should recover the hash of the nonce.
	got := new(big.Int).Exp(sig, big.NewInt(int64(pub.E)), pub.N)
	want := hashToRange(tok.KeyID, tok.Nonce, pub.N)
	if got.Cmp(want) != 0 {
		return ErrBadSignature
	}
	return nil
}

// SignBlinded performs the mint's half: it exponentiates a value it cannot
// read.
//
// The extra internal blinding is not redundant with the client's. The client
// blinds to hide the message from the mint; this blinds to keep the running
// time of the private-key operation independent of the key, because
// big.Int.Exp is not constant time. crypto/rsa does the same thing internally
// for the same reason.
func SignBlinded(priv *rsa.PrivateKey, blinded []byte) ([]byte, error) {
	if priv == nil || priv.N == nil {
		return nil, errors.New("mint: nil private key")
	}
	if priv.N.BitLen() < MinKeyBits {
		return nil, fmt.Errorf("mint: key is %d bits, refusing anything under %d", priv.N.BitLen(), MinKeyBits)
	}

	b := new(big.Int).SetBytes(blinded)
	if b.Sign() == 0 || b.Cmp(priv.N) >= 0 {
		return nil, fmt.Errorf("%w: outside the group", ErrDegenerate)
	}
	if isDegenerate(b, priv.N) {
		// 1 and n-1 are their own signatures, so signing them tells a caller
		// nothing they did not already know -- but accepting them means the
		// mint can be asked to spend an issuance on a value that was never
		// blinded, which is a free token.
		return nil, ErrDegenerate
	}

	n := priv.N
	e := big.NewInt(int64(priv.E))

	var v, vInv *big.Int
	for i := 0; ; i++ {
		if i > 64 {
			return nil, errors.New("mint: could not find an invertible signing blind")
		}
		candidate, err := rand.Int(rand.Reader, n)
		if err != nil {
			return nil, fmt.Errorf("mint: reading randomness: %w", err)
		}
		if candidate.Sign() == 0 {
			continue
		}
		inv := new(big.Int).ModInverse(candidate, n)
		if inv == nil {
			continue
		}
		v, vInv = candidate, inv
		break
	}

	// (b · v^e)^d = b^d · v, so multiplying by v⁻¹ afterwards recovers b^d
	// while the exponentiation itself ran on a value the attacker cannot
	// predict or control.
	masked := new(big.Int).Exp(v, e, n)
	masked.Mul(masked, b)
	masked.Mod(masked, n)

	sig := new(big.Int).Exp(masked, priv.D, n)
	sig.Mul(sig, vInv)
	sig.Mod(sig, n)

	return padTo(sig, byteLen(n)), nil
}

// isDegenerate reports values that would be signed to themselves.
func isDegenerate(v, n *big.Int) bool {
	if v.Cmp(big.NewInt(1)) == 0 {
		return true
	}
	nMinus1 := new(big.Int).Sub(n, big.NewInt(1))
	return v.Cmp(nMinus1) == 0
}

func byteLen(n *big.Int) int { return (n.BitLen() + 7) / 8 }

// padTo left-pads to a fixed width, so a value with leading zero bytes does not
// serialise shorter and become a different-looking token.
func padTo(v *big.Int, size int) []byte {
	out := make([]byte, size)
	v.FillBytes(out)
	return out
}
