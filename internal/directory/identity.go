// Package directory describes relays and the signed documents that list them.
//
// Phase 2 requires a user to obtain a relay's address and pin out of band, from
// an operator they chose. That is the strongest trust story the system has --
// nobody stands between the user and the operator -- but it does not scale past
// one relay, and it gives the user no way to discover an alternative when their
// relay goes down.
//
// A directory fixes discovery, and it does so by MOVING trust rather than
// removing it: instead of trusting one operator they picked, the user trusts
// whoever signs the directory to report relay keys honestly. That is a real
// trade and it is why consensus documents require signatures from several
// independent authorities, and why manual pinning remains fully supported and
// always wins. A user who has a pin should keep using it.
package directory

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// KeyPrefix marks an encoded Ed25519 public key, so a key in a config file is
// recognisable and cannot be confused with a TLS pin.
const KeyPrefix = "ed25519:"

// Identity is a long-term signing key.
//
// It is separate from the relay's TLS key on purpose. The TLS key can be
// rotated -- a new certificate for the same identity is a routine event -- but
// the identity key is what a client remembers, and rotating it means the relay
// is a different relay as far as anyone who pinned it is concerned.
type Identity struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// GenerateIdentity creates a new signing key.
func GenerateIdentity() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("directory: generating identity: %w", err)
	}
	return &Identity{Public: pub, Private: priv}, nil
}

// Fingerprint returns the encoded public key, which is what appears in
// documents and in configuration.
func (id *Identity) Fingerprint() string {
	if id == nil {
		return ""
	}
	return EncodeKey(id.Public)
}

// Sign signs a message.
func (id *Identity) Sign(msg []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(id.Private, msg))
}

// EncodeKey renders a public key for a document or a config file.
func EncodeKey(pub ed25519.PublicKey) string {
	return KeyPrefix + base64.StdEncoding.EncodeToString(pub)
}

// DecodeKey parses a public key, tolerating a missing prefix so an operator
// copying one out of a log does not have to be careful about it.
func DecodeKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("directory: empty key")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, KeyPrefix))
	if err != nil {
		return nil, fmt.Errorf("directory: key %q is not valid base64: %w", s, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("directory: key %q decodes to %d bytes, want %d", s, len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// VerifySignature checks a base64 signature over msg.
func VerifySignature(pub ed25519.PublicKey, msg []byte, sig string) bool {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, msg, raw)
}

// WriteIdentity saves a private key, 0600, refusing to overwrite.
//
// Replacing an identity silently would make a relay unrecognisable to everyone
// who had pinned it, which should never be the accidental outcome of restarting
// a daemon with the wrong flag.
func WriteIdentity(id *Identity, path string) error {
	der, err := marshalPKCS8(id.Private)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("directory: creating identity file: %w", err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// LoadIdentity reads a private key.
func LoadIdentity(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("directory: %s is not PEM", path)
	}
	priv, err := parsePKCS8Ed25519(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("directory: parsing %s: %w", path, err)
	}
	return &Identity{Public: priv.Public().(ed25519.PublicKey), Private: priv}, nil
}

// LoadOrCreateIdentity returns the identity at path, creating one if absent.
// The bool reports whether a new key was generated, so the caller can tell the
// operator to publish the new fingerprint.
func LoadOrCreateIdentity(path string) (*Identity, bool, error) {
	id, err := LoadIdentity(path)
	if err == nil {
		return id, false, nil
	}
	if !os.IsNotExist(err) {
		return nil, false, err
	}
	id, err = GenerateIdentity()
	if err != nil {
		return nil, false, err
	}
	if err := WriteIdentity(id, path); err != nil {
		return nil, false, err
	}
	return id, true, nil
}

// marshalPKCS8 and parsePKCS8Ed25519 wrap the standard library so identity
// files use the same PKCS#8 PEM shape as the TLS keys next to them, rather
// than a bespoke format an operator would have no tools for.
func marshalPKCS8(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("directory: marshalling identity: %w", err)
	}
	return der, nil
}

func parsePKCS8Ed25519(der []byte) (ed25519.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is %T, want ed25519.PrivateKey", key)
	}
	return priv, nil
}
