// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package utils

import (
	"crypto/rand"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/soheilhy/cmux"
)

// Limits on the node's web server. Without them one host could hold thousands
// of connections open by sending a byte at a time (or nothing at all), or
// stream an endless request, until the node runs out of connections or
// memory. There is no write limit: a handshake waits on chain queries, each
// bounded by rpc_query_timeout, and its reply is small.
const (
	firstBytesTimeout = 10 * time.Second // to send the bytes that tell TLS from plain HTTP
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = time.Minute
	maxHeaderBytes    = 16 << 10
)

// debugWriter hands each line the HTTP server logs about a connection to the
// node's logger at debug level. Left to itself the server prints them to
// stderr whatever the node's log level, and they name the remote address
// ("http: TLS handshake error from 203.0.113.9:4242: EOF"), which a node at the
// default level must not keep.
type debugWriter struct{ log cmtlog.Logger }

func (w debugWriter) Write(p []byte) (int, error) {
	w.log.Debug("HTTP server", "message", strings.TrimSpace(string(p)))

	return len(p), nil
}

// newServer is the HTTP server for one side of the port (TLS or plain).
func newServer(handler http.Handler, logger cmtlog.Logger) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          log.New(debugWriter{logger}, "", 0),
	}
}

// Listener is the node's API port, bound, with its certificate loaded.
type Listener struct {
	l    net.Listener
	cert tls.Certificate
}

// ListenTLS loads the certificate and binds the API port. The start command
// calls it before the node goes active on the chain, so a missing certificate
// or a port in use stops the node first.
func ListenTLS(address, certFile, keyFile string) (*Listener, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}

	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	return &Listener{l: l, cert: cert}, nil
}

// Serve answers TLS and plain HTTP on the port until it fails.
func (l *Listener) Serve(handler http.Handler, logger cmtlog.Logger) error {
	return serve(l.l, l.cert, handler, logger)
}

// Close releases the port; it is closed already once Serve has returned.
func (l *Listener) Close() error {
	return l.l.Close()
}

// serve answers TLS and plain HTTP on l, telling them apart by the first
// bytes of each connection. It returns the first error of the three servers
// once all three have ended: the node then stops, its service first.
func serve(l net.Listener, cert tls.Certificate, handler http.Handler, logger cmtlog.Logger) error {
	var (
		mux    = cmux.New(l)
		tlsMux = mux.Match(cmux.TLS())
		anyMux = mux.Match(cmux.Any())
	)
	mux.SetReadTimeout(firstBytesTimeout)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{
			cert,
		},
		MinVersion: tls.VersionTLS12,
		Rand:       rand.Reader,
	}

	ended := make(chan error, 3)
	go func() { ended <- newServer(handler, logger).Serve(tls.NewListener(tlsMux, tlsConfig)) }()
	go func() { ended <- newServer(handler, logger).Serve(anyMux) }()
	go func() { ended <- mux.Serve() }()

	// Closing the port ends mux.Serve, which closes the two listeners it
	// feeds, so the first to end takes the others with it.
	err := <-ended
	_ = l.Close()
	<-ended
	<-ended

	return err
}
