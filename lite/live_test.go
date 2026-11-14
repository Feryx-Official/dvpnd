// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"os"
	"strconv"
	"strings"
	"testing"

	base "github.com/Feryx-Official/feryxhub/v12/types"
	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v3"
	cmtlog "github.com/cometbft/cometbft/libs/log"
)

// TestLiveChain talks to a real RPC endpoint and is skipped unless
// DVPND_LIVE_RPC is set (comma-separated remotes). Optional DVPND_LIVE_NODE
// (bech32 sentnode address), DVPND_LIVE_SESSION and DVPND_LIVE_SUBSCRIPTION
// (ids) exercise the decode paths for the corresponding objects. Read-only;
// no key, no transaction.
func TestLiveChain(t *testing.T) {
	remotes := os.Getenv("DVPND_LIVE_RPC")
	if remotes == "" {
		t.Skip("DVPND_LIVE_RPC not set")
	}

	base.GetConfig()
	c := NewDefaultClient().
		WithLogger(cmtlog.NewNopLogger()).
		WithQueryTimeout(15).
		WithRemotes(strings.Split(remotes, ","))

	np, err := c.QueryNodeParams()
	if err != nil {
		t.Fatalf("node params: %v", err)
	}
	if np.StatusTimeout <= 0 {
		t.Fatalf("node status_timeout not positive: %v", np.StatusTimeout)
	}
	t.Logf("node params: status_timeout=%v deposit=%s", np.StatusTimeout, np.Deposit)

	sp, err := c.QuerySessionParams()
	if err != nil {
		t.Fatalf("session params: %v", err)
	}
	t.Logf("session params: status_timeout=%v proof_verification=%v", sp.StatusTimeout, sp.ProofVerificationEnabled)

	if addr := os.Getenv("DVPND_LIVE_NODE"); addr != "" {
		nodeAddr, err := base.NodeAddressFromBech32(addr)
		if err != nil {
			t.Fatal(err)
		}
		node, err := c.QueryNode(nodeAddr)
		if err != nil {
			t.Fatalf("node: %v", err)
		}
		if node == nil || node.Address != addr {
			t.Fatalf("node %s not returned: %+v", addr, node)
		}
		t.Logf("node: status=%s remote_addrs=%v gigabyte_prices=%s", node.Status, node.RemoteAddrs, node.GetGigabytePrices())

		if _, err := c.QueryNode(base.NodeAddress(make([]byte, 20))); err != nil {
			t.Fatalf("unknown node must yield nil, nil; got error %v", err)
		}
	}

	if v := os.Getenv("DVPND_LIVE_SESSION"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		session, err := c.QuerySession(id)
		if err != nil {
			t.Fatalf("session: %v", err)
		}
		if session == nil {
			t.Fatalf("session %d not found", id)
		}
		switch s := session.(type) {
		case *nodetypes.Session:
			t.Logf("node session %d: acc=%s node=%s max_bytes=%s status=%s", s.ID, s.AccAddress, s.NodeAddress, s.MaxBytes, s.Status)
		case *subscriptiontypes.Session:
			t.Logf("subscription session %d: acc=%s node=%s subscription=%d status=%s", s.ID, s.AccAddress, s.NodeAddress, s.SubscriptionID, s.Status)
		default:
			t.Fatalf("unexpected session type %T", session)
		}
		if session.GetID() != id {
			t.Fatalf("id mismatch: %d", session.GetID())
		}
	}

	if v := os.Getenv("DVPND_LIVE_SUBSCRIPTION"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		sub, err := c.QuerySubscription(id)
		if err != nil {
			t.Fatalf("subscription: %v", err)
		}
		if sub == nil || sub.ID != id {
			t.Fatalf("subscription %d not returned: %+v", id, sub)
		}
		t.Logf("subscription %d: acc=%s plan=%d status=%s", sub.ID, sub.AccAddress, sub.PlanID, sub.Status)
	}
}
