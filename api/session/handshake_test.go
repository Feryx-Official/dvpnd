// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// signedBody builds a request exactly the way client apps do: base64 of the
// peer-request JSON, and a compact secp256k1 signature over
// 8-byte big-endian id || those JSON bytes.
func signedBody(t *testing.T, priv *secp256k1.PrivKey, id uint64, peer interface{}) *HandshakeBody {
	t.Helper()
	data, err := json.Marshal(peer)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := priv.Sign(append(sdk.Uint64ToBigEndian(id), data...))
	if err != nil {
		t.Fatal(err)
	}
	return &HandshakeBody{
		Data:      base64.StdEncoding.EncodeToString(data),
		ID:        id,
		PubKey:    pubKeyPrefix + base64.StdEncoding.EncodeToString(priv.PubKey().Bytes()),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
}

// Rules: [HS-1].
func TestVerifyHandshake(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	body := signedBody(t, priv, 42, map[string]string{"public_key": "abc"})

	addr, data, err := verifyHandshake(body)
	if err != nil {
		t.Fatal(err)
	}
	if !addr.Equals(sdk.AccAddress(priv.PubKey().Address())) {
		t.Fatalf("address mismatch: %s", addr)
	}
	if string(data) != `{"public_key":"abc"}` {
		t.Fatalf("unexpected data %q", data)
	}

	// Tampered id, data or key must all fail.
	tampered := *body
	tampered.ID++
	if _, _, err := verifyHandshake(&tampered); err == nil {
		t.Error("tampered id accepted")
	}
	tampered = *body
	tampered.Data = base64.StdEncoding.EncodeToString([]byte(`{"public_key":"abd"}`))
	if _, _, err := verifyHandshake(&tampered); err == nil {
		t.Error("tampered data accepted")
	}
	tampered = *body
	tampered.PubKey = pubKeyPrefix + base64.StdEncoding.EncodeToString(secp256k1.GenPrivKey().PubKey().Bytes())
	if _, _, err := verifyHandshake(&tampered); err == nil {
		t.Error("wrong key accepted")
	}
	tampered = *body
	tampered.PubKey = "ed25519:" + tampered.PubKey[len(pubKeyPrefix):]
	if _, _, err := verifyHandshake(&tampered); err == nil {
		t.Error("wrong key type accepted")
	}
}
