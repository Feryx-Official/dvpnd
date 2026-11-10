// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"context"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/feegrant"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// WithGranter makes the client act for another account: the node account
// (granter) that gave this client's key, a hot key, the right to send the
// node's messages (authz) and to have its fees paid (feegrant). Every
// transaction is then one MsgExec carrying the node's messages, signed by the
// hot key with the granter as fee granter. The operator key and the
// earnings stay off the server.
func (c *Client) WithGranter(v sdk.AccAddress) *Client {
	c.granter = v
	return c
}

// Granter is the node account the client acts for; nil when its own key is
// the node account.
func (c *Client) Granter() sdk.AccAddress { return c.granter }

// execMessages wraps messages in one MsgExec when the client acts for a
// granter, and leaves them as they are otherwise.
func (c *Client) execMessages(messages []sdk.Msg) []sdk.Msg {
	if c.granter == nil {
		return messages
	}

	exec := authz.NewMsgExec(c.FromAddress(), messages)

	return []sdk.Msg{&exec}
}

// QueryGrantExpiry asks whether granter has authorised grantee to send
// messages of msgTypeURL. found is false when there is no such grant; expiry
// is nil for a grant that does not expire.
func (c *Client) QueryGrantExpiry(granter, grantee sdk.AccAddress, msgTypeURL string) (found bool, expiry *time.Time, err error) {
	c.log.Info("Querying the authz grant", "granter", granter, "grantee", grantee, "msg", msgTypeURL)
	err = c.query("authz grant", func(ctx client.Context) error {
		resp, err := authz.NewQueryClient(ctx).Grants(
			context.TODO(),
			&authz.QueryGrantsRequest{Granter: granter.String(), Grantee: grantee.String(), MsgTypeUrl: msgTypeURL},
		)
		if noAuthorization(err) {
			return nil
		}
		if err != nil {
			return types.QueryError(err)
		}
		if len(resp.Grants) > 0 {
			found, expiry = true, resp.Grants[0].Expiration
		}

		return nil
	})

	return found, expiry, err
}

// noAuthorization reports the chain's answer to a Grants query for a message
// type with no grant: an error, not an empty list, and with code Unknown rather
// than NotFound (authz.ErrNoAuthorizationFound carries no gRPC code). Seen on
// mainnet: "rpc error: code = Unknown desc = authorization not found for
// <type> type: authorization not found: unknown request".
func noAuthorization(err error) bool {
	return err != nil && strings.Contains(err.Error(), authz.ErrNoAuthorizationFound.Error())
}

// QueryFeeAllowance returns the fee allowance granter gave grantee, or nil.
// It lists the grantee's allowances rather than asking for the one: the chain
// answers a missing allowance with an internal error, not "not found".
func (c *Client) QueryFeeAllowance(granter, grantee sdk.AccAddress) (result feegrant.FeeAllowanceI, err error) {
	c.log.Info("Querying the fee allowance", "granter", granter, "grantee", grantee)
	err = c.query("fee allowance", func(ctx client.Context) error {
		resp, err := feegrant.NewQueryClient(ctx).Allowances(
			context.TODO(),
			&feegrant.QueryAllowancesRequest{Grantee: grantee.String()},
		)
		if err != nil {
			return types.QueryError(err)
		}
		for _, grant := range resp.Allowances {
			if grant.Granter == granter.String() {
				return c.ctx.InterfaceRegistry.UnpackAny(grant.Allowance, &result)
			}
		}

		return nil
	})

	return result, err
}
