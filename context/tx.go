// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package context

import (
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	nodetypes "github.com/sentinel-official/sentinelhub/v12/x/node/types/v3"
	sessiontypes "github.com/sentinel-official/sentinelhub/v12/x/session/types/v3"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

func (c *Context) RegisterNode() error {
	c.Log().Info("Registering the node...")

	_, err := c.Client().Tx(
		nodetypes.NewMsgRegisterNodeRequest(
			c.Operator(),
			c.GigabytePrices(),
			c.HourlyPrices(),
			c.RemoteAddrs(),
		),
	)
	if err != nil {
		c.Log().Error("failed to register the node", "error", err)
		return err
	}

	return nil
}

func (c *Context) UpdateNodeInfo() error {
	c.Log().Info("Updating the node info...")

	_, err := c.Client().Tx(
		nodetypes.NewMsgUpdateNodeDetailsRequest(
			c.Address(),
			c.GigabytePrices(),
			c.HourlyPrices(),
			c.RemoteAddrs(),
		),
	)
	if err != nil {
		c.Log().Error("failed to update the node info", "error", err)
		return err
	}

	return nil
}

func (c *Context) UpdateNodeStatus() error {
	c.Log().Info("Updating the node status...")

	_, err := c.Client().Tx(
		nodetypes.NewMsgUpdateNodeStatusRequest(
			c.Address(),
			v1base.StatusActive,
		),
	)
	if err != nil {
		c.Log().Error("failed to update the node status", "error", err)
		return err
	}

	return nil
}

func (c *Context) UpdateSessions(items ...types.Session) error {
	c.Log().Info("Updating the sessions...")

	messages := make([]sdk.Msg, 0, len(items))
	for _, item := range items {
		// The proof signature is the client's and is only checked by the chain
		// when its proof_verification_enabled parameter is on; the node has no
		// client signature to attach, so it sends none (as upstream did).
		messages = append(messages,
			sessiontypes.NewMsgUpdateSessionRequest(
				c.Address(),
				item.ID,
				sdkmath.NewInt(item.Download),
				sdkmath.NewInt(item.Upload),
				item.Duration(),
				nil,
			),
		)
	}

	_, err := c.Client().Tx(
		messages...,
	)
	if err != nil {
		c.Log().Error("failed to update the sessions", "error", err)
		return err
	}

	return nil
}
