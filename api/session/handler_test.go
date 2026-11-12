// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/gin-gonic/gin"
)

// handshakeService is the admission fake that also speaks the handshake: it
// takes the peer request as the peer data and names the peer in the payload.
type handshakeService struct{ *fakeService }

func (s handshakeService) ParsePeerRequest(raw []byte) ([]byte, error) { return raw, nil }
func (s handshakeService) HandshakePayload(result []byte) (interface{}, error) {
	return map[string]string{"peer": string(result)}, nil
}

// TestHandshakeReplyIsSigned drives a whole handshake as a client makes it:
// the reply carries the node's signature in its header, the signature checks
// out the way a client checks it, and the body keeps the two keys clients
// accept.
//
// Rules: [HS-5].
func TestHandshakeReplyIsSigned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, service, _, _ := admissionRig(t, 50)
	ctx.WithService(handshakeService{service})
	ctx.Config().Node.IPv4Address = "203.0.113.1"

	key := secp256k1.GenPrivKey()
	account := sdk.AccAddress(key.PubKey().Address())
	chain := &fakeChain{node: ctx.Address().String(), account: func(uint64) string { return account.String() }}

	request := []byte(`{"public_key":"abc"}`)
	sig, err := key.Sign(append(sdk.Uint64ToBigEndian(7), request...))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(HandshakeBody{
		Data:      base64.StdEncoding.EncodeToString(request),
		ID:        7,
		PubKey:    pubKeyPrefix + base64.StdEncoding.EncodeToString(key.PubKey().Bytes()),
		Signature: base64.StdEncoding.EncodeToString(sig),
	})

	r := gin.New()
	r.POST("/", handshake(ctx, chain, nodeSigner(t)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("handshake: %d %s", rec.Code, rec.Body)
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Result, &keys); err != nil || len(keys) != 2 || keys["data"] == nil || keys["addrs"] == nil {
		t.Fatalf("the result must hold exactly data and addrs: %s", envelope.Result)
	}
	var result HandshakeResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}

	header := rec.Header().Get(ReplySignatureHeader)
	if header == "" {
		t.Fatal("the reply carries no signature")
	}
	if !verifyReply(t, header, 7, request, &result) {
		t.Fatal("the reply's signature does not verify against the request and the reply")
	}
}

// requestAwareService is handshakeService for a protocol whose peer request
// asks for more than the peer data, as AmneziaWG's tier: it records the
// request admission hands it.
type requestAwareService struct {
	handshakeService
	requests *[]string
}

func (s requestAwareService) AddPeerForRequest(request, data []byte) ([]byte, error) {
	*s.requests = append(*s.requests, string(request))

	return s.AddPeer(data)
}

// TestHandshakeHandsTheRequestToAdmission: a handshake passes the client's
// peer request through admission to the service with the peer data, so a
// choice such as AmneziaWG's tier travels with its request and the service
// keeps nothing between parsing and admission.
//
// Rules: [CT-7].
func TestHandshakeHandsTheRequestToAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, service, _, _ := admissionRig(t, 50)
	var requests []string
	ctx.WithService(requestAwareService{handshakeService{service}, &requests})
	ctx.Config().Node.IPv4Address = "203.0.113.1"

	key := secp256k1.GenPrivKey()
	account := sdk.AccAddress(key.PubKey().Address())
	chain := &fakeChain{node: ctx.Address().String(), account: func(uint64) string { return account.String() }}

	request := []byte(`{"public_key":"abc","awg_version":3}`)
	sig, err := key.Sign(append(sdk.Uint64ToBigEndian(7), request...))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(HandshakeBody{
		Data:      base64.StdEncoding.EncodeToString(request),
		ID:        7,
		PubKey:    pubKeyPrefix + base64.StdEncoding.EncodeToString(key.PubKey().Bytes()),
		Signature: base64.StdEncoding.EncodeToString(sig),
	})

	r := gin.New()
	r.POST("/", handshake(ctx, chain, nodeSigner(t)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("handshake: %d %s", rec.Code, rec.Body)
	}
	if len(requests) != 1 || requests[0] != string(request) {
		t.Fatalf("the service was handed %q, want the client's request", requests)
	}
}
