// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// HandshakeBody is what current client apps POST to a node's root path. The
// client proves it holds the session's account key by signing the session id
// (8 bytes, big-endian) followed by the exact JSON bytes of the peer request,
// and sends the compressed secp256k1 public key alongside — so, unlike the
// legacy endpoint, the account needs no prior on-chain transaction.
type HandshakeBody struct {
	Data      string `json:"data"`
	ID        uint64 `json:"id"`
	PubKey    string `json:"pub_key"`
	Signature string `json:"signature"`
}

// HandshakeResult is the result envelope of a successful handshake: the
// service-specific configuration as base64 JSON, and the hosts the client can
// reach the node on.
type HandshakeResult struct {
	Data  string   `json:"data"`
	Addrs []string `json:"addrs"`
}

const pubKeyPrefix = "secp256k1:"

// verifyHandshake authenticates the body and returns the account address it
// was signed by together with the raw peer-request JSON.
func verifyHandshake(body *HandshakeBody) (sdk.AccAddress, []byte, error) {
	if !strings.HasPrefix(body.PubKey, pubKeyPrefix) {
		return nil, nil, errors.New("pub_key must be secp256k1")
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(body.PubKey, pubKeyPrefix))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid pub_key: %w", err)
	}
	if len(raw) != secp256k1.PubKeySize {
		return nil, nil, fmt.Errorf("pub_key must be %d bytes, got %d", secp256k1.PubKeySize, len(raw))
	}

	data, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid data: %w", err)
	}

	signature, err := base64.StdEncoding.DecodeString(body.Signature)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid signature: %w", err)
	}

	if body.ID == 0 {
		return nil, nil, errors.New("id cannot be zero")
	}

	var (
		pubKey  = &secp256k1.PubKey{Key: raw}
		message = append(sdk.Uint64ToBigEndian(body.ID), data...)
	)

	if !pubKey.VerifySignature(message, signature) {
		return nil, nil, errors.New("signature does not verify")
	}

	return sdk.AccAddress(pubKey.Address()), data, nil
}

// buildHandshakeResult renders the service configuration for the client.
func buildHandshakeResult(ctx *context.Context, peer []byte) (*HandshakeResult, error) {
	payload, err := ctx.Service().HandshakePayload(peer)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return &HandshakeResult{
		Data:  base64.StdEncoding.EncodeToString(data),
		Addrs: nodeAddrs(ctx),
	}, nil
}

// nodeAddrs lists the hosts a client may use to reach this node: the public
// IPv4 first, then the remote_url host when it differs (for example a DNS name).
func nodeAddrs(ctx *context.Context) []string {
	addrs := []string{ctx.IPv4Address().String()}
	if u, err := url.Parse(ctx.RemoteURL()); err == nil && u.Hostname() != "" && u.Hostname() != addrs[0] {
		addrs = append(addrs, u.Hostname())
	}

	return addrs
}

// HandlerHandshake serves POST / for current client apps.
func HandlerHandshake(ctx *context.Context) gin.HandlerFunc {
	return handshake(ctx, ctx.Client(), ctx.Client())
}

// handshake is HandlerHandshake with the chain it asks about the session and
// the key it signs the reply with given separately, so a test can stand in
// for both.
func handshake(ctx *context.Context, chain chainQuerier, signer replySigner) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body HandshakeBody
		if err := c.ShouldBindJSON(&body); err != nil {
			replyError(ctx, c, http.StatusBadRequest, 2, err)
			return
		}

		accAddr, data, err := verifyHandshake(&body)
		if err != nil {
			replyError(ctx, c, http.StatusBadRequest, 4, err)
			return
		}

		peerData, err := ctx.Service().ParsePeerRequest(data)
		if err != nil {
			replyError(ctx, c, http.StatusBadRequest, 2, err)
			return
		}

		res, apiErr := admit(ctx, chain, admitRequest{AccAddress: accAddr, ID: body.ID, PeerData: peerData})
		if apiErr != nil {
			replyError(ctx, c, apiErr.Status, apiErr.Code, apiErr.Err)
			return
		}

		result, err := buildHandshakeResult(ctx, res.Peer)
		if err != nil {
			replyError(ctx, c, http.StatusInternalServerError, 10, err)
			return
		}

		// The peer is admitted by now, so a reply that cannot be signed still
		// goes out; the client decides whether to use it.
		if header, err := signReply(signer, body.ID, data, result); err != nil {
			ctx.Log().Error("could not sign the handshake reply", "error", err)
		} else {
			c.Header(ReplySignatureHeader, header)
		}

		c.JSON(http.StatusOK, types.NewResponseResult(result))
	}
}
