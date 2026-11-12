// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The node is its own certificate authority: it issues the server certificate
// once and a client certificate per session. Everything is ECDSA P-256, which
// is what the client apps' TLS cipher requires.

const (
	caValidity     = 10 * 365 * 24 * time.Hour
	serverValidity = 10 * 365 * 24 * time.Hour

	// tlsCryptKeyLen is an OpenVPN static key V1: 2048 bits.
	tlsCryptKeyLen = 256
)

// pki is the node's certificate authority and the material the server runs on.
type pki struct {
	dir string

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caDER  []byte

	tlsCrypt []byte // the raw tls-crypt key, handed to clients
}

// Files under the pki directory.
func (p *pki) caCertPath() string     { return filepath.Join(p.dir, "ca.crt") }
func (p *pki) caKeyPath() string      { return filepath.Join(p.dir, "ca.key") }
func (p *pki) serverCertPath() string { return filepath.Join(p.dir, "server.crt") }
func (p *pki) serverKeyPath() string  { return filepath.Join(p.dir, "server.key") }
func (p *pki) tlsCryptPath() string   { return filepath.Join(p.dir, "tc.key") }

// loadOrCreatePKI reads the authority from dir, creating it on the first run.
// The CA, server certificate and tls-crypt key persist across restarts so
// that nothing about the node changes from a client's point of view.
func loadOrCreatePKI(dir string) (*pki, error) {
	p := &pki{dir: dir}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	if _, err := os.Stat(p.caKeyPath()); err != nil {
		if err := p.create(); err != nil {
			return nil, fmt.Errorf("creating the certificate authority: %w", err)
		}
	}

	return p, p.load()
}

func (p *pki) create() error {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "dvpnd OpenVPN CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serverDER, err := issue(caCert, caKey, &serverKey.PublicKey, "dvpnd OpenVPN server",
		now.Add(serverValidity), x509.ExtKeyUsageServerAuth)
	if err != nil {
		return err
	}

	tlsCrypt := make([]byte, tlsCryptKeyLen)
	if _, err := rand.Read(tlsCrypt); err != nil {
		return err
	}

	files := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{p.caCertPath(), certPEM(caDER), 0644},
		{p.caKeyPath(), mustKeyPEM(caKey), 0600},
		{p.serverCertPath(), certPEM(serverDER), 0644},
		{p.serverKeyPath(), mustKeyPEM(serverKey), 0600},
		{p.tlsCryptPath(), staticKeyFile(tlsCrypt), 0600},
	}
	for _, f := range files {
		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			return err
		}
	}

	return nil
}

func (p *pki) load() error {
	caPEM, err := os.ReadFile(p.caCertPath())
	if err != nil {
		return err
	}
	block, _ := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("ca.crt is not a PEM certificate")
	}
	p.caDER = block.Bytes
	if p.caCert, err = x509.ParseCertificate(block.Bytes); err != nil {
		return err
	}

	keyPEM, err := os.ReadFile(p.caKeyPath())
	if err != nil {
		return err
	}
	block, _ = pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return errors.New("ca.key is not a PEM private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("ca.key is not an ECDSA key")
	}
	p.caKey = ecKey

	tc, err := os.ReadFile(p.tlsCryptPath())
	if err != nil {
		return err
	}
	if p.tlsCrypt, err = parseStaticKeyFile(tc); err != nil {
		return err
	}

	return nil
}

// issueClient makes a certificate and key for one peer, both DER: the
// certificate's common name carries the peer's identity. It is valid until
// the authority expires: a session has no fixed end, and OpenVPN checks the
// dates at every connect, so an earlier expiry would cut off a session that
// is still paid for. Who may connect is decided over the management
// interface (admit), not by the certificate's dates.
func (p *pki) issueClient(commonName string) (certDER, keyDER []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	certDER, err = issue(p.caCert, p.caKey, &key.PublicKey, commonName, p.caCert.NotAfter, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err = x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	return certDER, keyDER, nil
}

func issue(ca *x509.Certificate, caKey *ecdsa.PrivateKey, pub *ecdsa.PublicKey, cn string, notAfter time.Time, eku x509.ExtKeyUsage) ([]byte, error) {
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{eku},
		BasicConstraintsValid: true,
	}

	return x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err)
	}

	return n
}

func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func mustKeyPEM(key *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// staticKeyFile renders a key the way "openvpn --genkey" does: hex in lines
// of 32 characters between the Static key V1 markers.
func staticKeyFile(key []byte) []byte {
	var b strings.Builder
	b.WriteString("#\n# 2048 bit OpenVPN static key\n#\n-----BEGIN OpenVPN Static key V1-----\n")
	h := hex.EncodeToString(key)
	for i := 0; i < len(h); i += 32 {
		b.WriteString(h[i:i+32] + "\n")
	}
	b.WriteString("-----END OpenVPN Static key V1-----\n")

	return []byte(b.String())
}

func parseStaticKeyFile(data []byte) ([]byte, error) {
	var h strings.Builder
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "-----BEGIN"):
			in = true
		case strings.HasPrefix(line, "-----END"):
			in = false
		case in && line != "" && !strings.HasPrefix(line, "#"):
			h.WriteString(line)
		}
	}
	key, err := hex.DecodeString(h.String())
	if err != nil || len(key) != tlsCryptKeyLen {
		return nil, errors.New("tc.key is not a 2048 bit OpenVPN static key")
	}

	return key, nil
}
