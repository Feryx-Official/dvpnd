// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package node

import (
	sessiontypes "github.com/sentinel-official/sentinelhub/v12/x/session/types/v3"
	subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v3"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
	"github.com/Feryx-Official/dvpnd/v9/utils"
)

// chain is what the jobs ask of the chain and send to it; tests replace it.
type chain interface {
	QuerySession(id uint64) (sessiontypes.Session, error)
	QuerySubscription(id uint64) (*subscriptiontypes.Subscription, error)
	UpdateSessions(items ...types.Session) error
}

// contextChain is the node's own chain client behind the chain interface.
type contextChain struct{ *context.Context }

func (c contextChain) QuerySession(id uint64) (sessiontypes.Session, error) {
	return c.Client().QuerySession(id)
}

func (c contextChain) QuerySubscription(id uint64) (*subscriptiontypes.Subscription, error) {
	return c.Client().QuerySubscription(id)
}

type Node struct {
	*context.Context
	chain chain
}

func NewNode(ctx *context.Context) *Node {
	return &Node{Context: ctx, chain: contextChain{ctx}}
}

func (n *Node) Initialize() error {
	n.Log().Info("Initializing...")

	result, err := n.Client().QueryNode(n.Address())
	if err != nil {
		return err
	}

	if result == nil {
		return n.RegisterNode()
	}

	return n.UpdateNodeInfo()
}

// Start runs the jobs and serves the API on api, bound by the caller before the
// node went active, until the API server fails or a job panics; either error
// ends the node through the caller, which then stops the VPN service.
func (n *Node) Start(api *utils.Listener) error {
	errCh := make(chan error, 4)

	go n.runJob("set_sessions", n.IntervalSetSessions(), n.setSessions, errCh)
	go n.runJob("update_sessions", n.IntervalUpdateSessions(), n.updateSessions, errCh)
	go n.runJob("update_status", n.IntervalUpdateStatus(), n.UpdateNodeStatus, errCh)
	go func() { errCh <- api.Serve(n.Handler(), n.Log()) }()

	return <-errCh
}
