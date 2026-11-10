// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package session

import (
	"fmt"
	"net/http"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// HandlerAddSession is the legacy endpoint (POST /accounts/:acc_address/sessions/:id).
// The request is authenticated against the public key the chain holds for the
// account, so the account must have transacted before.
func HandlerAddSession(ctx *context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		req, err := NewRequestAddSession(c)
		if err != nil {
			replyError(ctx, c, http.StatusBadRequest, 2, err)
			return
		}

		account, err := ctx.Client().QueryAccount(req.AccAddress)
		if err != nil {
			replyError(ctx, c, http.StatusInternalServerError, 4, err)
			return
		}
		if account == nil {
			err = fmt.Errorf("account %s does not exist", req.AccAddress)
			replyError(ctx, c, http.StatusNotFound, 4, err)
			return
		}
		if account.GetPubKey() == nil {
			err = fmt.Errorf("public key for account %s does not exist", req.AccAddress)
			replyError(ctx, c, http.StatusNotFound, 4, err)
			return
		}

		if ok := account.GetPubKey().VerifySignature(sdk.Uint64ToBigEndian(req.URI.ID), req.Signature); !ok {
			err = fmt.Errorf("invalid signature %s", req.Signature)
			replyError(ctx, c, http.StatusBadRequest, 4, err)
			return
		}

		res, apiErr := admit(ctx, ctx.Client(), admitRequest{
			AccAddress: req.AccAddress,
			ID:         req.URI.ID,
			PeerData:   req.Key,
		})
		if apiErr != nil {
			replyError(ctx, c, apiErr.Status, apiErr.Code, apiErr.Err)
			return
		}

		result := append([]byte{}, res.Peer...)
		result = append(result, ctx.IPv4Address()...)
		result = append(result, ctx.Service().Info()...)
		c.JSON(http.StatusCreated, types.NewResponseResult(result))
	}
}
