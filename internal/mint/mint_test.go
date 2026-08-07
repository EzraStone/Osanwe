package mint

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Key generation at 2048 bits is slow enough that doing it per test would
// dominate the run, and every test wants the same thing: one valid key.
var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := GenerateKey(MinKeyBits)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

func issue(t *testing.T, m *Mint) (*Token, []byte, []byte) {
	t.Helper()
	bl, err := Blind(m.PublicKey())
	if err != nil {
		t.Fatalf("Blind: %v", err)
	}
	blindSig, err := m.Issue(context.Background(), nil, bl.Blinded)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	tok, err := bl.Unblind(blindSig)
	if err != nil {
		t.Fatalf("Unblind: %v", err)
	}
	return tok, bl.Blinded, blindSig
}

func newMint(t *testing.T) *Mint {
	t.Helper()
	m, err := New(key(t), OpenAuthorizer{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// --------------------------------------------------------------------------
// the property the whole design rests on
// --------------------------------------------------------------------------

// TestEveryPairingIsAlgebraicallyConsistent shows that the mint's transcript
// is compatible with every possible assignment of tokens to buyers: for any
// recorded issuance and any redeemed token, a blinding factor exists that
// connects them.
//
//	given (b, s) from the mint's log and any redeemed (nonce, σ)
//	set r = s · σ⁻¹ mod n
//	then H(nonce) · r^e = b, so that pairing is valid
//
// This is a necessary condition and not a sufficient one, which is worth being
// blunt about: the identity holds because RSA is a bijection, so it is still
// true even if the client never blinded anything at all. Deleting the blinding
// entirely leaves this test passing. TestTheObviousLinkingAttacksFail is what
// actually holds the property up; this one only rules out an implementation
// where the algebra does not close.
func TestEveryPairingIsAlgebraicallyConsistent(t *testing.T) {
	m := newMint(t)
	pub := m.PublicKey()
	n := pub.N
	e := big.NewInt(int64(pub.E))

	const buyers = 6
	type record struct{ blinded, blindSig []byte }
	var log []record
	var tokens []*Token

	for i := 0; i < buyers; i++ {
		tok, blinded, blindSig := issue(t, m)
		log = append(log, record{blinded, blindSig})
		tokens = append(tokens, tok)
	}

	// Every pairing, not just the true one, has to check out.
	for i, rec := range log {
		b := new(big.Int).SetBytes(rec.blinded)
		s := new(big.Int).SetBytes(rec.blindSig)

		for j, tok := range tokens {
			sig := new(big.Int).SetBytes(tok.Sig)
			sigInv := new(big.Int).ModInverse(sig, n)
			if sigInv == nil {
				t.Fatalf("token %d has a non-invertible signature", j)
			}

			// The blinding factor that would connect issuance i to token j.
			r := new(big.Int).Mul(s, sigInv)
			r.Mod(r, n)

			// If that r is genuine, it reproduces exactly the blinded message
			// the mint was handed.
			want := hashToRange(tok.KeyID, tok.Nonce, n)
			got := new(big.Int).Exp(r, e, n)
			got.Mul(got, want)
			got.Mod(got, n)

			if got.Cmp(b) != 0 {
				t.Fatalf("issuance %d cannot be explained as producing token %d. "+
					"That asymmetry is exactly what would let the mint identify a buyer from a spent token", i, j)
			}
		}
	}
}

// TestTheObviousLinkingAttacksFail is the load-bearing test. It plays the mint
// trying to work out which buyer spent which token, using the two attacks that
// actually work against a broken implementation.
//
// A mint holds (b, s) per issuance and later sees (nonce, σ) per redemption.
// If blinding is absent or ineffective then b is just H(nonce) and s is just
// σ, and matching them is a dictionary lookup. Deleting the blinding from
// Blind makes both of these fail, which is precisely what the algebraic
// consistency test above cannot detect.
func TestTheObviousLinkingAttacksFail(t *testing.T) {
	m := newMint(t)
	pub := m.PublicKey()

	const buyers = 6
	type record struct{ blinded, blindSig []byte }
	var log []record
	var tokens []*Token

	for i := 0; i < buyers; i++ {
		tok, blinded, blindSig := issue(t, m)
		log = append(log, record{blinded, blindSig})
		tokens = append(tokens, tok)
	}

	for i, rec := range log {
		b := new(big.Int).SetBytes(rec.blinded)
		s := new(big.Int).SetBytes(rec.blindSig)

		for j, tok := range tokens {
			// Attack one: is the blinded message simply the hash of a nonce
			// that later showed up at redemption?
			if h := hashToRange(tok.KeyID, tok.Nonce, pub.N); b.Cmp(h) == 0 {
				t.Fatalf("issuance %d handed the mint H(nonce) of token %d directly; "+
					"the mint can link every buyer to their token with a lookup table", i, j)
			}
			// Attack two: is the signature the mint returned the same value
			// that later came back to be spent?
			if sig := new(big.Int).SetBytes(tok.Sig); s.Cmp(sig) == 0 {
				t.Fatalf("the signature the mint issued at %d is byte-identical to token %d as spent; "+
					"unblinding is not changing anything", i, j)
			}
		}
	}
}

// The same nonce blinded twice must produce unrelated values. Otherwise a buyer
// who requested two tokens would be linkable across those issuances even if
// neither could be tied to a redemption.
func TestTheSameNonceBlindsDifferentlyEachTime(t *testing.T) {
	pub := &key(t).PublicKey
	nonce := bytes.Repeat([]byte{0xab}, 32)

	first, err := blindNonce(pub, nonce)
	if err != nil {
		t.Fatalf("blindNonce: %v", err)
	}
	second, err := blindNonce(pub, nonce)
	if err != nil {
		t.Fatalf("blindNonce: %v", err)
	}
	if bytes.Equal(first.Blinded, second.Blinded) {
		t.Fatal("blinding the same nonce twice produced the same value; the blinding factor is not random")
	}

	// And neither reveals the nonce's hash.
	h := hashToRange(KeyID(pub), nonce, pub.N)
	for _, bl := range []*Blinding{first, second} {
		if new(big.Int).SetBytes(bl.Blinded).Cmp(h) == 0 {
			t.Fatal("the blinded value equals H(nonce); nothing is being hidden from the mint")
		}
	}
}

// A message blinded twice must not produce the same blinded value, or two
// issuances by the same buyer would be linkable to each other even if neither
// is linkable to a redemption.
func TestBlindingIsFreshEveryTime(t *testing.T) {
	pub := &key(t).PublicKey
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		bl, err := Blind(pub)
		if err != nil {
			t.Fatalf("Blind: %v", err)
		}
		s := string(bl.Blinded)
		if seen[s] {
			t.Fatal("two blindings produced the same value")
		}
		seen[s] = true
	}
}

// The mint never sees the token, so the token's bytes must not appear in what
// the mint holds. Weaker than the test above, but it fails loudly if the
// protocol is ever "simplified" into sending the nonce in the clear.
func TestNothingTheMintSeesContainsTheToken(t *testing.T) {
	m := newMint(t)
	tok, blinded, blindSig := issue(t, m)

	for _, seen := range [][]byte{blinded, blindSig} {
		if bytes.Contains(seen, tok.Nonce) {
			t.Fatal("the nonce appears in what the mint received; it is supposed to have never seen it")
		}
		if bytes.Contains(seen, tok.Sig) {
			t.Fatal("the finished signature appears in the mint's view")
		}
	}
}

// --------------------------------------------------------------------------
// issuance and verification
// --------------------------------------------------------------------------

func TestIssuedTokenVerifies(t *testing.T) {
	m := newMint(t)
	tok, _, _ := issue(t, m)
	if err := Verify(m.PublicKey(), tok); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if tok.KeyID != m.KeyID() {
		t.Fatalf("token names key %s, mint is %s", tok.KeyID, m.KeyID())
	}
	if got := m.Issued(); got != 1 {
		t.Fatalf("Issued() = %d, want 1", got)
	}
}

func TestForgedTokenIsRejected(t *testing.T) {
	m := newMint(t)
	tok, _, _ := issue(t, m)

	t.Run("tampered nonce", func(t *testing.T) {
		bad := &Token{KeyID: tok.KeyID, Nonce: append([]byte(nil), tok.Nonce...), Sig: tok.Sig}
		bad.Nonce[0] ^= 1
		if err := Verify(m.PublicKey(), bad); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("Verify = %v, want ErrBadSignature", err)
		}
	})

	t.Run("tampered signature", func(t *testing.T) {
		bad := &Token{KeyID: tok.KeyID, Nonce: tok.Nonce, Sig: append([]byte(nil), tok.Sig...)}
		bad.Sig[len(bad.Sig)-1] ^= 1
		if err := Verify(m.PublicKey(), bad); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("Verify = %v, want ErrBadSignature", err)
		}
	})

	t.Run("invented from nothing", func(t *testing.T) {
		bad := &Token{KeyID: tok.KeyID, Nonce: []byte("not a real nonce"), Sig: make([]byte, len(tok.Sig))}
		bad.Sig[len(bad.Sig)-1] = 2
		if err := Verify(m.PublicKey(), bad); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("Verify = %v, want ErrBadSignature", err)
		}
	})
}

// A token must not be movable between mint keys. Without domain separation in
// the hash, a signature made under one key would verify under another whose
// modulus happened to admit it, and rotation would stop retiring anything.
func TestTokenIsBoundToTheKeyThatSignedIt(t *testing.T) {
	m := newMint(t)
	tok, _, _ := issue(t, m)

	other, err := GenerateKey(MinKeyBits)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := Verify(&other.PublicKey, tok); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a token verified under a key that did not sign it: %v", err)
	}

	// And relabelling it does not help.
	relabelled := &Token{KeyID: KeyID(&other.PublicKey), Nonce: tok.Nonce, Sig: tok.Sig}
	if err := Verify(&other.PublicKey, relabelled); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a relabelled token verified: %v", err)
	}
}

func TestDegenerateBlindedValuesAreRefused(t *testing.T) {
	priv := key(t)
	n := priv.N
	size := (n.BitLen() + 7) / 8

	cases := map[string]*big.Int{
		"zero":  big.NewInt(0),
		"one":   big.NewInt(1),
		"n-1":   new(big.Int).Sub(n, big.NewInt(1)),
		"n":     new(big.Int).Set(n),
		"above": new(big.Int).Add(n, big.NewInt(7)),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			buf := make([]byte, size)
			if v.BitLen() <= size*8 {
				v.FillBytes(buf)
			} else {
				buf = v.Bytes()
			}
			if _, err := SignBlinded(priv, buf); err == nil {
				t.Fatal("signed a degenerate value; that is a token nobody had to blind for")
			}
		})
	}
}

func TestBlindingCannotBeReused(t *testing.T) {
	m := newMint(t)
	bl, err := Blind(m.PublicKey())
	if err != nil {
		t.Fatalf("Blind: %v", err)
	}
	sig, err := m.Issue(context.Background(), nil, bl.Blinded)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := bl.Unblind(sig); err != nil {
		t.Fatalf("Unblind: %v", err)
	}
	if _, err := bl.Unblind(sig); err == nil {
		t.Fatal("unblinded twice; reusing a blinding factor makes both issuances linkable to each other")
	}
}

// --------------------------------------------------------------------------
// payment
// --------------------------------------------------------------------------

type refusing struct{}

func (refusing) Authorize(context.Context, []byte) error { return errors.New("no entitlement") }

func TestUnpaidIssuanceIsRefused(t *testing.T) {
	m, err := New(key(t), refusing{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bl, err := Blind(m.PublicKey())
	if err != nil {
		t.Fatalf("Blind: %v", err)
	}
	if _, err := m.Issue(context.Background(), nil, bl.Blinded); !errors.Is(err, ErrNotPaid) {
		t.Fatalf("Issue = %v, want ErrNotPaid", err)
	}
	if got := m.Issued(); got != 0 {
		t.Fatalf("Issued() = %d after a refused request, want 0", got)
	}
}

func TestMintRequiresAnExplicitAuthorizer(t *testing.T) {
	if _, err := New(key(t), nil); err == nil {
		t.Fatal("built a mint with no Authorizer; an accidentally open mint prints money")
	}
}

// --------------------------------------------------------------------------
// double spending
// --------------------------------------------------------------------------

func TestTokenCannotBeSpentTwice(t *testing.T) {
	m := newMint(t)
	tok, _, _ := issue(t, m)
	spent := NewSpentSet()

	if err := spent.Spend(tok); err != nil {
		t.Fatalf("first spend: %v", err)
	}
	if err := spent.Spend(tok); !errors.Is(err, ErrAlreadySpent) {
		t.Fatalf("second spend = %v, want ErrAlreadySpent", err)
	}
	if got := spent.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
}

func TestDistinctTokensSpendIndependently(t *testing.T) {
	m := newMint(t)
	spent := NewSpentSet()
	for i := 0; i < 5; i++ {
		tok, _, _ := issue(t, m)
		if err := spent.Spend(tok); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if got := spent.Len(); got != 5 {
		t.Fatalf("Len() = %d, want 5", got)
	}
}

func TestRetireDropsOnlyTheNamedKey(t *testing.T) {
	m := newMint(t)
	spent := NewSpentSet()
	tok, _, _ := issue(t, m)
	if err := spent.Spend(tok); err != nil {
		t.Fatalf("Spend: %v", err)
	}
	other := &Token{KeyID: "mint-someotherkey", Nonce: []byte("n"), Sig: []byte("s")}
	if err := spent.Spend(other); err != nil {
		t.Fatalf("Spend: %v", err)
	}

	if n := spent.Retire(m.KeyID()); n != 1 {
		t.Fatalf("Retire dropped %d, want 1", n)
	}
	if got := spent.Len(); got != 1 {
		t.Fatalf("Len() = %d after retiring one key, want 1", got)
	}
}

func TestSpendIsSafeUnderConcurrency(t *testing.T) {
	m := newMint(t)
	tok, _, _ := issue(t, m)
	spent := NewSpentSet()

	const racers = 32
	var wg sync.WaitGroup
	results := make([]error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = spent.Spend(tok)
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for _, err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("%d of %d concurrent spends succeeded, want exactly 1", accepted, racers)
	}
}

// --------------------------------------------------------------------------
// key storage
// --------------------------------------------------------------------------

func TestKeyRoundTripsAndIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mint.key")

	priv, created, err := LoadOrCreateKey(path, MinKeyBits)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if !created {
		t.Fatal("reported an existing key in an empty directory")
	}

	again, created, err := LoadOrCreateKey(path, MinKeyBits)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if created {
		t.Fatal("generated a second key rather than loading the one on disk")
	}
	if priv.N.Cmp(again.N) != 0 {
		t.Fatal("loaded a different key than was written")
	}

	if err := WriteKey(priv, path); err == nil {
		t.Fatal("overwrote an existing signing key; every token issued under it would become unverifiable")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file mode is %o, want 600", perm)
	}
}

func TestUndersizedKeysAreRefused(t *testing.T) {
	if _, err := GenerateKey(1024); err == nil {
		t.Fatal("generated a 1024-bit mint key; factoring it later mints unlimited free tokens")
	}
}

// TestRefillingDoesNotCancelASpend guards a bug found by watching a live
// wallet: the background refill used Put, which exists to reverse a Take, so
// every token bought in the background quietly un-counted a request that had
// already happened. The interface showed zero spent after a request that had
// plainly gone through.
func TestRefillingDoesNotCancelASpend(t *testing.T) {
	m := newMint(t)
	srv := httptest.NewServer(NewServer(m, quietLog()).Handler())
	defer srv.Close()

	w := NewWallet(&Client{URL: srv.URL, ExpectKeyID: m.KeyID()}, "", 4)

	// Take one, which also empties the wallet and triggers a refill.
	if _, err := w.Take(context.Background()); err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got := w.Spent(); got != 1 {
		t.Fatalf("Spent() = %d immediately after one Take, want 1", got)
	}

	// Refill in the foreground, so the assertion does not race a goroutine.
	w.fill(context.Background())
	if w.Len() == 0 {
		t.Fatal("the wallet did not refill")
	}
	if got := w.Spent(); got != 1 {
		t.Fatalf("Spent() = %d after a refill, want 1: buying tokens must not un-count a request", got)
	}
}

// Returning a token that was never handed over does reverse the spend, which
// is the case Put exists for.
func TestReturningAnUnusedTokenReversesTheSpend(t *testing.T) {
	m := newMint(t)
	srv := httptest.NewServer(NewServer(m, quietLog()).Handler())
	defer srv.Close()

	w := NewWallet(&Client{URL: srv.URL, ExpectKeyID: m.KeyID()}, "", 4)
	tok, err := w.Take(context.Background())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got := w.Spent(); got != 1 {
		t.Fatalf("Spent() = %d, want 1", got)
	}
	w.Put(tok)
	if got := w.Spent(); got != 0 {
		t.Fatalf("Spent() = %d after returning an unused token, want 0", got)
	}
}
