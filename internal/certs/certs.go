// Package certs handles the TLS identity of a ranger.
//
// The link between bearer and ranger is itself TLS, which is not merely
// belt-and-braces. A CONNECT request names its target in the clear, so an
// unencrypted client-to-relay hop would let anyone watching the client's
// uplink read "CONNECT api.anthropic.com:443" and learn exactly which provider
// is being used -- defeating a good part of the point of relaying at all.
//
// Relay identity is pinned by public-key fingerprint rather than delegated to
// the web PKI. A volunteer running a node on a bare IP has no domain to get a
// certificate for, and requiring one would exclude most operators. Pinning the
// SPKI also survives certificate renewal, so an operator can rotate their
// certificate without every client having to be reconfigured.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"crypto/tls"
)

// PinPrefix marks a fingerprint string, matching the convention used by HPKP
// and by Go's own tooling. Keeping the prefix makes a pin recognisable in a
// config file and impossible to confuse with some other base64 blob.
const PinPrefix = "sha256/"

// Pin returns the fingerprint of a certificate's public key: the base64
// SHA-256 of its SubjectPublicKeyInfo.
//
// The public key is fingerprinted rather than the whole certificate so that
// renewing a certificate with the same key does not invalidate every client's
// configuration.
func Pin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return PinPrefix + base64.StdEncoding.EncodeToString(sum[:])
}

// NormalizePin accepts a pin with or without the "sha256/" prefix and returns
// the canonical form, so an operator copying a fingerprint out of a log does
// not have to be careful about it.
func NormalizePin(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("certs: empty pin")
	}
	raw := strings.TrimPrefix(s, PinPrefix)
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("certs: pin %q is not valid base64: %w", s, err)
	}
	if len(decoded) != sha256.Size {
		return "", fmt.Errorf("certs: pin %q decodes to %d bytes, want %d", s, len(decoded), sha256.Size)
	}
	return PinPrefix + base64.StdEncoding.EncodeToString(decoded), nil
}

// SelfSigned generates a fresh P-256 key and a self-signed certificate valid
// for the given hosts, returning the certificate and its pin.
//
// This is how a volunteer operator without a domain gets started: run the
// relay, copy the pin it prints, hand that to clients. It is a complete
// deployment path, not a development shortcut -- pinning makes a self-signed
// certificate exactly as strong as a CA-issued one here, and avoids depending
// on the CA system to vouch for a relay it knows nothing about.
func SelfSigned(hosts []string, validFor time.Duration) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("certs: generating key: %w", err)
	}

	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("certs: generating serial: %w", err)
	}

	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "osanwe-ranger"},
		NotBefore:             now.Add(-time.Hour), // tolerate modest clock skew
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("certs: creating certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("certs: parsing generated certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}, Pin(leaf), nil
}

// Load reads a certificate and key from PEM files and returns them with the
// leaf's pin, so an operator using a real certificate can publish a pin too.
func Load(certPath, keyPath string) (tls.Certificate, string, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("certs: loading keypair: %w", err)
	}
	if cert.Leaf == nil {
		if len(cert.Certificate) == 0 {
			return tls.Certificate{}, "", fmt.Errorf("certs: %s contains no certificate", certPath)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, "", fmt.Errorf("certs: parsing leaf: %w", err)
		}
		cert.Leaf = leaf
	}
	return cert, Pin(cert.Leaf), nil
}

// WritePEM saves a certificate and its key, so a generated identity survives a
// restart. Without this a relay would present a new pin every time it started
// and every client would break.
//
// The key file is created 0600. If it already exists the write fails rather
// than truncating: silently replacing a relay's identity would invalidate
// every client's pin, and that should never happen by accident.
func WritePEM(cert tls.Certificate, certPath, keyPath string) error {
	if len(cert.Certificate) == 0 {
		return fmt.Errorf("certs: nothing to write")
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return fmt.Errorf("certs: marshalling key: %w", err)
	}

	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("certs: creating key file: %w", err)
	}
	defer keyFile.Close()
	if err := pem.Encode(keyFile, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		return fmt.Errorf("certs: writing key: %w", err)
	}

	certFile, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("certs: creating certificate file: %w", err)
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}); err != nil {
		return fmt.Errorf("certs: writing certificate: %w", err)
	}
	return nil
}

// LoadOrCreate returns the identity stored at the given paths, generating and
// saving one if it is not there yet. Reported bool is true when a new identity
// was created, so the caller can tell the operator to distribute the new pin.
func LoadOrCreate(certPath, keyPath string, hosts []string, validFor time.Duration) (tls.Certificate, string, bool, error) {
	cert, pin, err := Load(certPath, keyPath)
	if err == nil {
		return cert, pin, false, nil
	}
	if !os.IsNotExist(unwrapPathError(err)) {
		return tls.Certificate{}, "", false, err
	}

	cert, pin, err = SelfSigned(hosts, validFor)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	if err := WritePEM(cert, certPath, keyPath); err != nil {
		return tls.Certificate{}, "", false, err
	}
	return cert, pin, true, nil
}

// unwrapPathError digs out an *os.PathError so os.IsNotExist can see it
// through the wrapping done above.
func unwrapPathError(err error) error {
	for err != nil {
		if pe, ok := err.(*os.PathError); ok {
			return pe
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
	return err
}
