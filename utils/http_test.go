// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"
)

// Rules: [HS-10].
func TestNewServerHasLimits(t *testing.T) {
	s := newServer(http.NotFoundHandler(), cmtlog.NewNopLogger())
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.IdleTimeout == 0 || s.MaxHeaderBytes == 0 || s.ErrorLog == nil {
		t.Fatalf("every server limit and the error log must be set: %+v", s)
	}
}

// syncBuffer is a bytes.Buffer the server's goroutines can write to while the
// test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// TestServerMessagesGoToTheDebugLog: a connection that fails its TLS handshake
// makes the HTTP server log the remote address. That line must reach the
// node's logger at debug level, not stderr, so a node at the default level
// keeps no client addresses.
//
// Rules: [PV-1].
func TestServerMessagesGoToTheDebugLog(t *testing.T) {
	var buf syncBuffer
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.Config = newServer(http.NotFoundHandler(), cmtlog.NewTMLogger(&buf))
	srv.StartTLS()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")) // plain HTTP to the TLS side
	_ = conn.Close()

	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(buf.String(), "TLS handshake error") {
			break
		}
	}
	out := buf.String()
	if !strings.Contains(out, "TLS handshake error") {
		t.Fatalf("the handshake error must reach the node's logger: %q", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "D[") {
			t.Fatalf("the server's own messages are debug lines only: %q", line)
		}
	}
}

// selfSigned is a throwaway certificate for 127.0.0.1.
func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestServeEndsWhenTheListenerCloses: once the port's listener ends, serve
// returns its error and the TLS and plain servers on it have ended too. They
// panicked, which ended the process before the start command could stop the
// service.
//
// Rules: [RT-2].
func TestServeEndsWhenTheListenerCloses(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- serve(l, selfSigned(t), http.NotFoundHandler(), cmtlog.NewNopLogger()) }()

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // a throwaway certificate
	}
	for _, scheme := range []string{"https", "http"} {
		res, err := client.Get(scheme + "://" + l.Addr().String() + "/")
		if err != nil {
			t.Fatalf("%s: %v", scheme, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404 from the handler", scheme, res.StatusCode)
		}
	}
	client.CloseIdleConnections()

	_ = l.Close()
	select {
	case err := <-served:
		if err == nil {
			t.Error("serve returned no error when its listener closed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve still runs 5 s after its listener closed")
	}
	time.Sleep(200 * time.Millisecond) // a server that outlived serve and panics does so here
}

// TestListenTLSRefusesWhatCannotServe: a missing certificate or a port in use
// is an error from ListenTLS, which the start command calls before the node
// goes active on the chain.
//
// Rules: [RT-16].
func TestListenTLSRefusesWhatCannotServe(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, err := ListenTLS("127.0.0.1:0", certFile, keyFile); err == nil {
		t.Error("ListenTLS without a certificate succeeded")
	}

	cert := selfSigned(t)
	der, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if _, err := ListenTLS(busy.Addr().String(), certFile, keyFile); err == nil {
		t.Error("ListenTLS on a port in use succeeded")
	}

	l, err := ListenTLS("127.0.0.1:0", certFile, keyFile)
	if err != nil {
		t.Fatalf("ListenTLS with a certificate and a free port: %v", err)
	}
	_ = l.Close()
}
