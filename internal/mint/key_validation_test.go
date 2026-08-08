package mint

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMalformedPublicModuliAreRejectedBeforeCrypto(t *testing.T) {
	valid := &key(t).PublicKey
	negative := new(big.Int).Neg(new(big.Int).Set(valid.N))
	even := new(big.Int).Add(new(big.Int).Set(valid.N), big.NewInt(1))

	cases := []struct {
		name string
		pub  *rsa.PublicKey
		want string
	}{
		{name: "nil key", pub: nil, want: "nil public key"},
		{name: "nil modulus", pub: &rsa.PublicKey{E: valid.E}, want: "nil public key"},
		{name: "zero modulus", pub: &rsa.PublicKey{N: new(big.Int), E: valid.E}, want: "modulus must be positive"},
		{name: "negative modulus", pub: &rsa.PublicKey{N: negative, E: valid.E}, want: "modulus must be positive"},
		{name: "even modulus", pub: &rsa.PublicKey{N: even, E: valid.E}, want: "modulus must be odd"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePublicKey(tc.pub); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validatePublicKey error = %v, want %q", err, tc.want)
			}
			if _, err := Blind(tc.pub); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Blind error = %v, want %q", err, tc.want)
			}
			if err := ValidateBlinded(tc.pub, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateBlinded error = %v, want %q", err, tc.want)
			}
			if err := Verify(tc.pub, &Token{}); !errors.Is(err, ErrBadSignature) {
				t.Fatalf("Verify error = %v, want ErrBadSignature", err)
			}
			if _, err := marshalPublicPEM(tc.pub); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("marshalPublicPEM error = %v, want %q", err, tc.want)
			}
			path := filepath.Join(t.TempDir(), "mint.pub")
			if err := WritePublicKey(tc.pub, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("WritePublicKey error = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid public key created an output file: %v", err)
			}
		})
	}
}

func TestMalformedPrivateModuliAreRejectedBeforeCrypto(t *testing.T) {
	valid := key(t)
	withModulus := func(n *big.Int) *rsa.PrivateKey {
		copy := *valid
		copy.PublicKey = rsa.PublicKey{N: n, E: valid.E}
		return &copy
	}
	cases := []struct {
		name string
		priv *rsa.PrivateKey
		want string
	}{
		{name: "nil key", priv: nil, want: "key"},
		{name: "nil modulus", priv: withModulus(nil), want: "nil public key"},
		{name: "zero modulus", priv: withModulus(new(big.Int)), want: "modulus must be positive"},
		{name: "negative modulus", priv: withModulus(new(big.Int).Neg(new(big.Int).Set(valid.N))), want: "modulus must be positive"},
		{name: "even modulus", priv: withModulus(new(big.Int).Add(new(big.Int).Set(valid.N), big.NewInt(1))), want: "modulus must be odd"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.priv, OpenAuthorizer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New error = %v, want %q", err, tc.want)
			}
			if _, err := SignBlinded(tc.priv, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SignBlinded error = %v, want %q", err, tc.want)
			}
			path := filepath.Join(t.TempDir(), "mint.key")
			if err := WriteKey(tc.priv, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("WriteKey error = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid private key created an output file: %v", err)
			}
		})
	}
}

func TestPublicExponentValidationRejectsUnsafeRange(t *testing.T) {
	valid := key(t).PublicKey
	exponents := []int{-3, 0, 2, 4}
	if strconv.IntSize > 32 {
		exponents = append(exponents, int(int64(1)<<31))
	}
	for _, exponent := range exponents {
		pub := &rsa.PublicKey{N: new(big.Int).Set(valid.N), E: exponent}
		if err := validatePublicKey(pub); err == nil || !strings.Contains(err.Error(), "public exponent") {
			t.Errorf("E=%d: error = %v, want public-exponent rejection", exponent, err)
		}
	}
}

func TestLoadPublicKeyRejectsEvenModulus(t *testing.T) {
	valid := key(t).PublicKey
	pub := &rsa.PublicKey{
		N: new(big.Int).Add(new(big.Int).Set(valid.N), big.NewInt(1)),
		E: valid.E,
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mint.pub")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMType, Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing malformed public key fixture: %v", err)
	}

	if _, err := LoadPublicKey(path); err == nil || !strings.Contains(err.Error(), "modulus must be odd") {
		t.Fatalf("LoadPublicKey error = %v, want odd-modulus rejection", err)
	}
}

func TestBlindingCopiesValidatedPublicKey(t *testing.T) {
	pub := clonePublicKey(&key(t).PublicKey)
	blinding, err := Blind(pub)
	if err != nil {
		t.Fatalf("Blind: %v", err)
	}

	pub.N.Neg(pub.N)
	if blinding.pub.N.Sign() <= 0 {
		t.Fatal("mutating the caller's modulus changed the validated blinding key")
	}
}

func TestMintPublicKeyReturnsDefensiveCopy(t *testing.T) {
	m := newMint(t)
	pub := m.PublicKey()
	pub.N.Neg(pub.N)

	if got := m.PublicKey().N.Sign(); got <= 0 {
		t.Fatalf("mutating the returned public key changed the mint key: sign = %d", got)
	}
}
