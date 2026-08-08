package mint

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalTokenIsAStandardRSAPSSSignature(t *testing.T) {
	m := newMint(t)
	tok, _, _ := issue(t, m)

	digest := sha512.Sum384(tok.Nonce)
	err := rsa.VerifyPSS(m.PublicKey(), crypto.SHA384, digest[:], tok.Sig, &rsa.PSSOptions{
		Hash:       crypto.SHA384,
		SaltLength: crypto.SHA384.Size(),
	})
	if err != nil {
		t.Fatalf("the RFC 9474 result was not a standard SHA-384 RSA-PSS signature: %v", err)
	}
}

func TestPreparedTokenIsDomainSeparatedAndRandomized(t *testing.T) {
	pub := &key(t).PublicKey
	nonce := make([]byte, tokenNonceBytes)
	first, err := blindNonce(pub, nonce)
	if err != nil {
		t.Fatalf("first blind: %v", err)
	}
	second, err := blindNonce(pub, nonce)
	if err != nil {
		t.Fatalf("second blind: %v", err)
	}

	if string(first.prepared[preparePrefixBytes:]) != string(tokenMessage(KeyID(pub), nonce)) {
		t.Fatal("prepared message is missing the protocol/key domain")
	}
	if string(first.prepared[:preparePrefixBytes]) == string(second.prepared[:preparePrefixBytes]) {
		t.Fatal("two preparations reused the randomized prefix")
	}
}

type recordingAuthorizer struct{ calls int }

func (a *recordingAuthorizer) Authorize(context.Context, []byte) error {
	a.calls++
	return nil
}

func TestMalformedBlindDoesNotConsumeAuthorization(t *testing.T) {
	auth := new(recordingAuthorizer)
	m, err := New(key(t), auth)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	malformed := make([]byte, byteLen(m.PublicKey().N))
	if _, err := m.Issue(context.Background(), []byte("one-shot-receipt"), malformed); err == nil {
		t.Fatal("mint accepted a zero blinded message")
	}
	if auth.calls != 0 {
		t.Fatalf("Authorize was called %d times for malformed protocol input", auth.calls)
	}
}

func TestLegacyKeyFilesAreRejectedInsteadOfReusedAcrossProtocols(t *testing.T) {
	dir := t.TempDir()
	priv := key(t)

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	privatePath := filepath.Join(dir, "legacy.key")
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if _, err := LoadKey(privatePath); err == nil || !strings.Contains(err.Error(), "dedicated key") {
		t.Fatalf("LoadKey legacy error = %v, want dedicated-key refusal", err)
	}

	der, err = x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	publicPath := filepath.Join(dir, "legacy.pub")
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	if _, err := LoadPublicKey(publicPath); err == nil || !strings.Contains(err.Error(), "dedicated RFC 9474 key") {
		t.Fatalf("LoadPublicKey legacy error = %v, want dedicated-key refusal", err)
	}
}

func FuzzParseTokenCanonical(f *testing.F) {
	f.Add("")
	f.Add("mint-example.bad.signature")
	f.Add("mint-example...signature")

	f.Fuzz(func(t *testing.T, encoded string) {
		tok, err := ParseToken(encoded)
		if err != nil {
			return
		}
		if got := tok.Encode(); got != encoded {
			t.Fatalf("accepted non-canonical token %q and re-encoded it as %q", encoded, got)
		}
	})
}
