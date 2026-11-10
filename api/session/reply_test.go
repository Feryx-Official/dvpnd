// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Feryx-Official/dvpnd/v9/lite"
)

func nodeSigner(t *testing.T) *lite.Client {
	t.Helper()

	kr := keyring.NewInMemory(lite.DefaultEncodingConfig().Codec)
	if _, _, err := kr.NewMnemonic("node", keyring.English, sdk.FullFundraiserPath,
		keyring.DefaultBIP39Passphrase, hd.Secp256k1); err != nil {
		t.Fatal(err)
	}

	return lite.NewDefaultClient().WithKeyring(kr).WithFromName("node")
}

// verifyReply is what a client does with the header: split it, and check the
// signature over the digest it computes from its own request and the reply.
func verifyReply(t *testing.T, header string, id uint64, request []byte, result *HandshakeResult) bool {
	t.Helper()

	key, sig, ok := strings.Cut(strings.TrimPrefix(header, pubKeyPrefix), ";")
	if !ok || !strings.HasPrefix(header, pubKeyPrefix) {
		t.Fatalf("header %q", header)
	}
	rawKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatal(err)
	}
	rawSig, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(rawSig) != 64 {
		t.Fatalf("signature %q: %v", sig, err)
	}
	reply, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		t.Fatal(err)
	}

	pub := &secp256k1.PubKey{Key: rawKey}
	return pub.VerifySignature(replyDigest(id, request, reply, result.Addrs), rawSig)
}

// Rules: [HS-5].
func TestSignedReply(t *testing.T) {
	signer := nodeSigner(t)
	request := []byte(`{"public_key":"abc"}`)
	result := &HandshakeResult{
		Data:  base64.StdEncoding.EncodeToString([]byte(`{"addrs":["10.8.0.2/32"],"metadata":[{"port":51820}]}`)),
		Addrs: []string{"203.0.113.1", "node.example"},
	}

	header, err := signReply(signer, 42, request, result)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyReply(t, header, 42, request, result) {
		t.Fatal("a genuine reply does not verify")
	}

	// The signing key is the node's own.
	if key, _ := signerPubKey(t, signer); !strings.Contains(header, base64.StdEncoding.EncodeToString(key)) {
		t.Fatal("header does not carry the node's key")
	}

	// Anything changed on the way, or a reply meant for another request,
	// fails.
	for name, tamper := range map[string]func() (uint64, []byte, *HandshakeResult){
		"another session": func() (uint64, []byte, *HandshakeResult) { return 43, request, result },
		"another request": func() (uint64, []byte, *HandshakeResult) { return 42, []byte(`{"public_key":"abd"}`), result },
		"swapped reply data": func() (uint64, []byte, *HandshakeResult) {
			r := *result
			r.Data = base64.StdEncoding.EncodeToString([]byte(`{"addrs":["10.8.0.3/32"],"metadata":[{"port":51820}]}`))
			return 42, request, &r
		},
		"swapped address": func() (uint64, []byte, *HandshakeResult) {
			r := *result
			r.Addrs = []string{"198.51.100.9", "node.example"}
			return 42, request, &r
		},
	} {
		id, req, res := tamper()
		if verifyReply(t, header, id, req, res) {
			t.Errorf("%s: verified", name)
		}
	}
}

func signerPubKey(t *testing.T, c *lite.Client) ([]byte, error) {
	t.Helper()
	_, pub, err := c.SignBytes([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	return pub.Bytes(), nil
}

// TestReplyDigestVector pins the signed digest to the bytes docs/protocols.md
// specifies, computed outside Go: clients verify against the spec, so any
// change to the digest breaks every client that checks it. tools/e2e checks
// the same vector with its own implementation.
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
