// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package context

import (
	sdkmath "cosmossdk.io/math"
	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	sessiontypes "github.com/Feryx-Official/feryxhub/v12/x/session/types/v3"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// The messages the node sends are built apart from their sending, so that
// what the node builds can be checked against the chain's frozen wire and
// signer contract (contract_test.go).

func (c *Context) registerNodeMsg() sdk.Msg {
	return nodetypes.NewMsgRegisterNodeRequest(c.Operator(), c.GigabytePrices(), c.HourlyPrices(), c.RemoteAddrs())
}

func (c *Context) nodeDetailsMsg() sdk.Msg {
	return nodetypes.NewMsgUpdateNodeDetailsRequest(c.Address(), c.GigabytePrices(), c.HourlyPrices(), c.RemoteAddrs())
}

func (c *Context) nodeStatusMsg() sdk.Msg {
	return nodetypes.NewMsgUpdateNodeStatusRequest(c.Address(), v1base.StatusActive)
}

// sessionReportMsg carries no proof signature: the signature is the client's
// and is only checked by the chain when its proof_verification_enabled
// parameter is on, and the node has no client signature to attach (as
// upstream did not).
func (c *Context) sessionReportMsg(r types.Report) sdk.Msg {
	return sessiontypes.NewMsgUpdateSessionRequest(c.Address(), r.ID, sdkmath.NewInt(r.Download), sdkmath.NewInt(r.Upload), r.Duration, nil)
}

func (c *Context) RegisterNode() error {
	c.Log().Info("Registering the node...")

	_, err := c.Client().Tx(c.registerNodeMsg())
	if err != nil {
		c.Log().Error("failed to register the node", "error", err)
		return err
	}

	return nil
}

func (c *Context) UpdateNodeInfo() error {
	c.Log().Info("Updating the node info...")

	_, err := c.Client().Tx(c.nodeDetailsMsg())
	if err != nil {
		c.Log().Error("failed to update the node info", "error", err)
		return err
	}

	return nil
}

func (c *Context) UpdateNodeStatus() error {
	c.Log().Info("Updating the node status...")

	_, err := c.Client().Tx(c.nodeStatusMsg())
	if err != nil {
		c.Log().Error("failed to update the node status", "error", err)
		return err
	}

	return nil
}

func (c *Context) UpdateSessions(reports ...types.Report) error {
	c.Log().Info("Updating the sessions...")

	messages := make([]sdk.Msg, 0, len(reports))
	for _, r := range reports {
		messages = append(messages, c.sessionReportMsg(r))
	}

	_, err := c.Client().Tx(messages...)
	if err != nil {
		c.Log().Error("failed to update the sessions", "error", err)
		return err
	}

	return nil
}
