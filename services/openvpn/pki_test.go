// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Rules: [CT-4].
func TestPKICreateLoadIssue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "openvpn")

	p, err := loadOrCreatePKI(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"ca.crt", "ca.key", "server.crt", "server.key", "tc.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if len(p.tlsCrypt) != tlsCryptKeyLen {
		t.Fatalf("tls-crypt key: %d bytes", len(p.tlsCrypt))
	}

	// A second load finds the same authority.
	again, err := loadOrCreatePKI(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.caDER, p.caDER) || !bytes.Equal(again.tlsCrypt, p.tlsCrypt) {
		t.Fatal("reload changed the authority")
	}

	certDER, keyDER, err := p.issueClient("01020304-0506-0708-090a-0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "01020304-0506-0708-090a-0b0c0d0e0f10" {
		t.Fatalf("CN: %s", cert.Subject.CommonName)
	}
	roots := x509.NewCertPool()
	roots.AddCert(p.caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("client certificate does not verify: %v", err)
	}
	if _, err := x509.ParsePKCS8PrivateKey(keyDER); err != nil {
		t.Fatalf("client key is not PKCS#8: %v", err)
	}

	serverPEM, _ := os.ReadFile(filepath.Join(dir, "server.crt"))
	block := decodeFirstPEM(serverPEM)
	server, err := x509.ParseCertificate(block)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("server certificate does not verify: %v", err)
	}

	tc, _ := os.ReadFile(filepath.Join(dir, "tc.key"))
	if !bytes.Contains(tc, []byte("-----BEGIN OpenVPN Static key V1-----")) {
		t.Fatalf("tc.key format:\n%s", tc)
	}
	parsed, err := parseStaticKeyFile(tc)
	if err != nil || !bytes.Equal(parsed, p.tlsCrypt) {
		t.Fatalf("static key round trip: %v", err)
	}
}

func decodeFirstPEM(data []byte) []byte {
	start := bytes.Index(data, []byte("-----BEGIN CERTIFICATE-----\n"))
	end := bytes.Index(data, []byte("-----END CERTIFICATE-----"))
	body := data[start+len("-----BEGIN CERTIFICATE-----\n") : end]
	out := make([]byte, 0, len(body))
	// base64 decode via the pem package would be simpler, but keep the test free of it
	dec, _ := base64Decode(string(bytes.ReplaceAll(body, []byte("\n"), nil)))
	return append(out, dec...)
}

// TestClientCertificateOutlivesTheSession: a session on the chain has no
// fixed end, and OpenVPN checks the client certificate's dates at every
// connect and renegotiation, so the certificate is valid for as long as the
// node's authority. Who may connect is the management interface's decision
// (TestManagementAdmitStatusKill), not the certificate's expiry.
//
// Rules: [SL-19].
func TestClientCertificateOutlivesTheSession(t *testing.T) {
	p, err := loadOrCreatePKI(filepath.Join(t.TempDir(), "openvpn"))
	if err != nil {
		t.Fatal(err)
	}
	certDER, _, err := p.issueClient("01020304-0506-0708-090a-0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.NotAfter.Equal(p.caCert.NotAfter) {
		t.Fatalf("client certificate valid until %s, the authority until %s", cert.NotAfter, p.caCert.NotAfter)
	}
	roots := x509.NewCertPool()
	roots.AddCert(p.caCert)
	for _, at := range []time.Time{time.Now().Add(30 * 24 * time.Hour), p.caCert.NotAfter.Add(-time.Hour)} {
		_, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
		if err != nil {
			t.Errorf("at %s the client certificate does not verify: %v", at.Format(time.DateOnly), err)
		}
	}
}
