// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/Feryx-Official/feryxhub/v12/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
)

// The vector api/session's TestReplyDigestVector pins for the node.
//
// Rules: [HS-6].
func TestReplyDigestVector(t *testing.T) {
	got := replyDigest(42, []byte(`{"public_key":"abc"}`),
		[]byte(`{"addrs":["10.8.0.2/32"],"metadata":[{"port":51820}]}`),
		[]string{"203.0.113.1", "node.example"})
	if want := "8146f1e4b375b1e9ff799ccc91dc8304678a645c5e35b94575d33edd6d364882"; hex.EncodeToString(got) != want {
		t.Fatalf("digest %x, want %s", got, want)
	}
}

func TestCheckReplySignature(t *testing.T) {
	key := secp256k1.GenPrivKey()
	node := base.NodeAddress(key.PubKey().Address())
	request, reply, addrs := []byte(`{"uuid":"x"}`), []byte(`{"metadata":[]}`), []string{"203.0.113.1"}

	sig, err := key.Sign(replyDigest(7, request, reply, addrs))
	if err != nil {
		t.Fatal(err)
	}
	header := "secp256k1:" + base64.StdEncoding.EncodeToString(key.PubKey().Bytes()) + ";" +
		base64.StdEncoding.EncodeToString(sig)

	if signer, err := checkReplySignature(header, 7, request, reply, addrs, node); err != nil || signer != "the node account" {
		t.Fatalf("genuine reply: %q, %v", signer, err)
	}
	other := base.NodeAddress(secp256k1.GenPrivKey().PubKey().Address())
	if signer, err := checkReplySignature(header, 7, request, reply, addrs, other); err != nil || !strings.HasPrefix(signer, "hot key") {
		t.Fatalf("another node's key: %q, %v", signer, err)
	}
	if _, err := checkReplySignature(header, 7, request, reply, []string{"198.51.100.1"}, node); err == nil {
		t.Fatal("a swapped address verified")
	}
	if _, err := checkReplySignature("", 7, request, reply, addrs, node); err == nil {
		t.Fatal("a reply without a signature passed")
	}
}

// The driver talks to a node API only when it presents the certificate it was
// given.
func TestTrustNodeCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	dir := t.TempDir()
	own := filepath.Join(dir, "own.crt")
	if err := os.WriteFile(own, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	otherPEM, _, err := common.SelfSignedCertificate("dvpnd", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.crt")
	if err := os.WriteFile(other, otherPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	get := func(path string) error {
		cfg, err := trustNodeCertificate(path)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(own); err != nil {
		t.Fatalf("the node's own certificate is refused: %v", err)
	}
	if err := get(other); err == nil {
		t.Fatal("a node presenting another certificate is trusted")
	}
	if _, err := trustNodeCertificate(filepath.Join(dir, "missing.crt")); err == nil {
		t.Fatal("a missing certificate file is accepted")
	}
}
