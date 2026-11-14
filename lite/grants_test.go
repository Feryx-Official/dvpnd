// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/feegrant"
)

// Rules: [CH-6].
func TestNodeMsgTypeURLs(t *testing.T) {
	want := []string{
		"/sentinel.node.v3.MsgRegisterNodeRequest",
		"/sentinel.node.v3.MsgUpdateNodeDetailsRequest",
		"/sentinel.node.v3.MsgUpdateNodeStatusRequest",
		"/sentinel.session.v3.MsgUpdateSessionRequest",
	}
	if strings.Join(NodeMsgTypeURLs, " ") != strings.Join(want, " ") || ExecMsgTypeURL != "/cosmos.authz.v1beta1.MsgExec" {
		t.Fatalf("type urls %v, exec %s", NodeMsgTypeURLs, ExecMsgTypeURL)
	}
}

func allGrants(expiry *time.Time) map[string]GrantState {
	grants := map[string]GrantState{}
	for _, url := range NodeMsgTypeURLs {
		grants[url] = GrantState{Found: true, Expiry: expiry}
	}
	return grants
}

func onlyExec(t *testing.T, inner feegrant.FeeAllowanceI) *feegrant.AllowedMsgAllowance {
	t.Helper()
	a, err := feegrant.NewAllowedMsgAllowance(inner, []string{ExecMsgTypeURL})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Rules: [CH-4].
func TestEvaluateGrants(t *testing.T) {
	var (
		now      = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
		year     = now.Add(365 * 24 * time.Hour)
		week     = now.Add(7 * 24 * time.Hour)
		past     = now.Add(-time.Hour)
		minSpend = sdk.NewCoins(sdk.NewInt64Coin("udvpn", 10_000_000))
		plenty   = sdk.NewCoins(sdk.NewInt64Coin("udvpn", 100_000_000))
	)

	for _, tc := range []struct {
		name      string
		grants    map[string]GrantState
		allowance feegrant.FeeAllowanceI
		problem   string
		warning   string
	}{
		{name: "all in order", grants: allGrants(&year),
			allowance: onlyExec(t, &feegrant.BasicAllowance{SpendLimit: plenty, Expiration: &year})},
		{name: "no expiry, no spend limit", grants: allGrants(nil),
			allowance: &feegrant.BasicAllowance{}},
		{name: "one grant missing",
			grants: func() map[string]GrantState {
				g := allGrants(&year)
				delete(g, NodeMsgTypeURLs[3])
				return g
			}(),
			allowance: &feegrant.BasicAllowance{}, problem: "no authz grant for /sentinel.session.v3.MsgUpdateSessionRequest"},
		{name: "grants expired", grants: allGrants(&past), allowance: &feegrant.BasicAllowance{}, problem: "expired at"},
		{name: "grants expire soon", grants: allGrants(&week), allowance: &feegrant.BasicAllowance{}, warning: "expires at"},
		{name: "no allowance", grants: allGrants(nil), problem: "no fee allowance"},
		{name: "allowance for other messages", grants: allGrants(nil),
			allowance: func() feegrant.FeeAllowanceI {
				a, _ := feegrant.NewAllowedMsgAllowance(&feegrant.BasicAllowance{}, []string{"/cosmos.bank.v1beta1.MsgSend"})
				return a
			}(), problem: "does not cover /cosmos.authz.v1beta1.MsgExec"},
		{name: "allowance expires soon", grants: allGrants(nil),
			allowance: onlyExec(t, &feegrant.BasicAllowance{Expiration: &week}), warning: "fee allowance expires"},
		{name: "allowance low", grants: allGrants(nil),
			allowance: &feegrant.BasicAllowance{SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("udvpn", 5_000_000))},
			warning:   "5000000udvpn left"},
		{name: "allowance spent", grants: allGrants(nil),
			allowance: &feegrant.PeriodicAllowance{Basic: feegrant.BasicAllowance{SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("uatom", 5))}},
			problem:   "0udvpn left"},
	} {
		problems, warnings := EvaluateGrants(now, tc.grants, tc.allowance, minSpend)
		got := strings.Join(problems, "; ")
		if (tc.problem == "") != (got == "") || !strings.Contains(got, tc.problem) {
			t.Errorf("%s: problems %q, want %q", tc.name, got, tc.problem)
		}
		gotW := strings.Join(warnings, "; ")
		if (tc.warning == "") != (gotW == "") || !strings.Contains(gotW, tc.warning) {
			t.Errorf("%s: warnings %q, want %q", tc.name, gotW, tc.warning)
		}
	}
}

// TestHotKeyTransactions: with a granter the node's messages travel in one
// MsgExec from the hot key, and the granter pays the fee; without one they
// go as they are.
//
// Rules: [CH-2].
func TestHotKeyTransactions(t *testing.T) {
	var (
		hot     = sdk.AccAddress([]byte("hot-key-address-0001"))
		granter = sdk.AccAddress([]byte("node-account-addr-01"))
		msg     = nodetypes.NewMsgUpdateNodeStatusRequest(granter.Bytes(), v1base.StatusActive)
	)

	plain := NewDefaultClient().WithFromAddress(hot)
	if got := plain.execMessages([]sdk.Msg{msg}); len(got) != 1 || got[0] != msg {
		t.Fatalf("without a granter: %v", got)
	}

	c := NewDefaultClient().WithFromAddress(hot).WithGranter(granter)
	got := c.execMessages([]sdk.Msg{msg})
	exec, ok := got[0].(*authz.MsgExec)
	if len(got) != 1 || !ok || exec.Grantee != hot.String() || len(exec.Msgs) != 1 {
		t.Fatalf("with a granter: %v", got)
	}
	inner, err := exec.GetMessages()
	if err != nil || len(inner) != 1 || !inner[0].GetSigners()[0].Equals(granter) {
		t.Fatalf("inner message %v: %v; it must be signed for the granter", inner, err)
	}
	if err := exec.ValidateBasic(); err != nil {
		t.Fatal(err)
	}

	// The transaction carries the granter as fee granter and encodes.
	txb, err := c.txf.WithChainID("sentinelhub-2").WithFeeGranter(granter).BuildUnsignedTx(got...)
	if err != nil {
		t.Fatal(err)
	}
	if !txb.GetTx().FeeGranter().Equals(granter) {
		t.Fatalf("fee granter %s", txb.GetTx().FeeGranter())
	}
	if _, err := c.TxConfig().TxEncoder()(txb.GetTx()); err != nil {
		t.Fatal(err)
	}
}

// TestFeeAllowanceFoundPastTheFirstPage: anyone can grant a fee allowance to
// any address, so a hot key can hold any number; the granter's is found on
// whichever page the chain lists it, and grants that sort before it cannot
// keep the node from starting.
//
// Rules: [CH-9].
func TestFeeAllowanceFoundPastTheFirstPage(t *testing.T) {
	chain, c := newFakeChain(t)
	for i := 0; i < 1000; i++ {
		chain.grantAllowance(sdk.AccAddress(fmt.Sprintf("dust-granter-%07d", i)), &feegrant.BasicAllowance{})
	}
	granter := sdk.AccAddress("node-account-addr-01")
	limit := sdk.NewCoins(sdk.NewInt64Coin("udvpn", 42))
	chain.grantAllowance(granter, &feegrant.BasicAllowance{SpendLimit: limit})

	got, err := c.QueryFeeAllowance(granter, c.FromAddress())
	if err != nil {
		t.Fatal(err)
	}
	if basic, ok := got.(*feegrant.BasicAllowance); !ok || !basic.SpendLimit.IsEqual(limit) {
		t.Fatalf("the granter's allowance, listed after 1000 others, came back as %v", got)
	}
}

// TestNoAuthorization: the chain answers a Grants query for a missing grant
// with an error, which must read as "no grant" so the node names the grant to
// make instead of failing on what looks like an RPC fault.
//
// Rules: [CH-5].
func TestNoAuthorization(t *testing.T) {
	mainnet := errors.New("rpc error: code = Unknown desc = authorization not found for " +
		"/sentinel.node.v3.MsgUpdateNodeStatusRequest type: authorization not found: unknown request")
	if !noAuthorization(mainnet) {
		t.Fatal("the chain's answer for a missing grant is not read as no grant")
	}
	for _, err := range []error{nil, errors.New("rpc error: code = Unavailable desc = connection refused")} {
		if noAuthorization(err) {
			t.Fatalf("%v read as no grant", err)
		}
	}
}
