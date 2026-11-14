// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package lite

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	base "github.com/Feryx-Official/feryxhub/v12/types"
	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	sessiontypes "github.com/Feryx-Official/feryxhub/v12/x/session/types/v3"
	v2subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v2"
	subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v3"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkquery "github.com/cosmos/cosmos-sdk/types/query"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// query runs fn against each configured RPC remote until one succeeds. fn
// returns nil both on success and when the chain answers "not found" (see
// types.QueryError), leaving the caller's result untouched in the latter case.
// When every remote fails the returned error names each of them, so a dead or
// redirecting endpoint is visible instead of hiding behind the last one tried.
func (c *Client) query(name string, fn func(ctx client.Context) error) error {
	return c.eachRemote("Query failed", func(remote string) error {
		rpc, err := c.rpc(remote, c.queryTimeout)
		if err != nil {
			return err
		}

		return fn(c.ctx.WithClient(rpc))
	}, "name", name)
}

// eachRemote calls fn with each RPC remote in turn until one succeeds. A
// remote that fails is logged with failed and keyvals, and the next one is
// tried: one that redirects or refuses must not stop the node while another
// works. When all fail, the error names every remote with its own error. A
// transaction's settled answer ends the loop: another remote cannot change
// what a block included or a mempool took.
func (c *Client) eachRemote(failed string, fn func(remote string) error, keyvals ...interface{}) error {
	var errs []error
	for _, remote := range c.remotes {
		err := fn(remote)
		if err == nil {
			return nil
		}

		c.log.Info(failed, append(append([]interface{}{}, keyvals...), "remote", remote, "error", err)...)
		errs = append(errs, fmt.Errorf("%s: %w", remote, err))
		if settled(err) {
			break
		}
	}

	return errors.Join(errs...)
}

func (c *Client) QueryAccount(accAddr sdk.AccAddress) (result authtypes.AccountI, err error) {
	c.log.Debug("Querying the account", "address", accAddr)
	err = c.query("account", func(ctx client.Context) error {
		resp, err := authtypes.NewQueryClient(ctx).Account(
			context.TODO(),
			&authtypes.QueryAccountRequest{Address: accAddr.String()},
		)
		if err != nil {
			return types.QueryError(err)
		}

		return c.ctx.InterfaceRegistry.UnpackAny(resp.Account, &result)
	})

	return result, err
}

func (c *Client) QueryNode(nodeAddr base.NodeAddress) (result *nodetypes.Node, err error) {
	c.log.Debug("Querying the node", "address", nodeAddr)
	err = c.query("node", func(ctx client.Context) error {
		resp, err := nodetypes.NewQueryServiceClient(ctx).QueryNode(
			context.TODO(),
			nodetypes.NewQueryNodeRequest(nodeAddr),
		)
		if err != nil {
			return types.QueryError(err)
		}

		result = &resp.Node
		return nil
	})

	return result, err
}

// QuerySession returns the session as the v3 Session interface; the concrete
// type is *nodetypes.Session (pay-per-session with this node, carries MaxBytes
// and MaxDuration) or *subscriptiontypes.Session (plan subscription, carries
// SubscriptionID). nil, nil means the chain no longer has the session.
func (c *Client) QuerySession(id uint64) (result sessiontypes.Session, err error) {
	// As text: a client's handshake names this id, and CodeQL counts any value
	// a client sent, a number too, as able to break a log line.
	c.log.Debug("Querying the session", "id", strconv.FormatUint(id, 10))
	err = c.query("session", func(ctx client.Context) error {
		resp, err := sessiontypes.NewQueryServiceClient(ctx).QuerySession(
			context.TODO(),
			sessiontypes.NewQuerySessionRequest(id),
		)
		if err != nil {
			return types.QueryError(err)
		}

		return c.ctx.InterfaceRegistry.UnpackAny(resp.Session, &result)
	})

	return result, err
}

func (c *Client) QuerySubscription(id uint64) (result *subscriptiontypes.Subscription, err error) {
	c.log.Debug("Querying the subscription", "id", id)
	err = c.query("subscription", func(ctx client.Context) error {
		resp, err := subscriptiontypes.NewQueryServiceClient(ctx).QuerySubscription(
			context.TODO(),
			subscriptiontypes.NewQuerySubscriptionRequest(id),
		)
		if err != nil {
			return types.QueryError(err)
		}

		result = &resp.Subscription
		return nil
	})

	return result, err
}

// QueryAllocation reads the per-account byte allocation of a plan subscription.
// Allocations are still served by the v2 query service at v12.
func (c *Client) QueryAllocation(id uint64, accAddr sdk.AccAddress) (result *v2subscriptiontypes.Allocation, err error) {
	c.log.Debug("Querying the allocation", "id", id, "address", accAddr)
	err = c.query("allocation", func(ctx client.Context) error {
		resp, err := v2subscriptiontypes.NewQueryServiceClient(ctx).QueryAllocation(
			context.TODO(),
			v2subscriptiontypes.NewQueryAllocationRequest(id, accAddr),
		)
		if err != nil {
			return types.QueryError(err)
		}

		result = &resp.Allocation
		return nil
	})

	return result, err
}

func (c *Client) QueryNodeParams() (result *nodetypes.Params, err error) {
	err = c.query("node_params", func(ctx client.Context) error {
		resp, err := nodetypes.NewQueryServiceClient(ctx).QueryParams(
			context.TODO(),
			nodetypes.NewQueryParamsRequest(),
		)
		if err != nil {
			return err
		}

		result = &resp.Params
		return nil
	})

	return result, err
}

func (c *Client) QuerySessionParams() (result *sessiontypes.Params, err error) {
	err = c.query("session_params", func(ctx client.Context) error {
		resp, err := sessiontypes.NewQueryServiceClient(ctx).QueryParams(
			context.TODO(),
			sessiontypes.NewQueryParamsRequest(),
		)
		if err != nil {
			return err
		}

		result = &resp.Params
		return nil
	})

	return result, err
}

// QuerySessionsForAccount lists the account's sessions, newest last as stored.
func (c *Client) QuerySessionsForAccount(accAddr sdk.AccAddress) (result []sessiontypes.Session, err error) {
	c.log.Debug("Querying the sessions for account", "address", accAddr)
	err = c.query("sessions_for_account", func(ctx client.Context) error {
		resp, err := sessiontypes.NewQueryServiceClient(ctx).QuerySessionsForAccount(
			context.TODO(),
			sessiontypes.NewQuerySessionsForAccountRequest(accAddr, &sdkquery.PageRequest{Limit: 500}),
		)
		if err != nil {
			return types.QueryError(err)
		}

		result = result[:0]
		for i := 0; i < len(resp.Sessions); i++ {
			var item sessiontypes.Session
			if err := c.ctx.InterfaceRegistry.UnpackAny(resp.Sessions[i], &item); err != nil {
				return err
			}
			result = append(result, item)
		}

		return nil
	})

	return result, err
}
