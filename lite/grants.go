// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"fmt"
	"slices"
	"time"

	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	sessiontypes "github.com/Feryx-Official/feryxhub/v12/x/session/types/v3"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/feegrant"
)

// NodeMsgTypeURLs are the messages a node sends, each of which a hot key
// needs a grant for.
var NodeMsgTypeURLs = []string{
	sdk.MsgTypeURL(&nodetypes.MsgRegisterNodeRequest{}),
	sdk.MsgTypeURL(&nodetypes.MsgUpdateNodeDetailsRequest{}),
	sdk.MsgTypeURL(&nodetypes.MsgUpdateNodeStatusRequest{}),
	sdk.MsgTypeURL(&sessiontypes.MsgUpdateSessionRequest{}),
}

// ExecMsgTypeURL is the one message a hot key sends, and the one its fee
// allowance should be limited to.
var ExecMsgTypeURL = sdk.MsgTypeURL(&authz.MsgExec{})

// GrantWarningWindow is how early the node warns that a grant expires.
const GrantWarningWindow = 14 * 24 * time.Hour

// GrantState is what the chain holds for one message type: whether there is
// a grant, and when it expires (nil: never).
type GrantState struct {
	Found  bool
	Expiry *time.Time
}

// EvaluateGrants says what stops a hot key from running the node (problems)
// and what will soon (warnings): a missing or expired grant for one of the
// node's messages; a missing, expired or exhausted fee allowance, or one
// that does not cover MsgExec. minSpend is what the node expects to spend on
// fees over GrantWarningWindow; an allowance below it is reported.
func EvaluateGrants(now time.Time, grants map[string]GrantState, allowance feegrant.FeeAllowanceI,
	minSpend sdk.Coins) (problems, warnings []string) {
	soon := now.Add(GrantWarningWindow)

	for _, url := range NodeMsgTypeURLs {
		g := grants[url]
		switch {
		case !g.Found:
			problems = append(problems, "no authz grant for "+url)
		case g.Expiry != nil && !g.Expiry.After(now):
			problems = append(problems, fmt.Sprintf("the authz grant for %s expired at %s", url, g.Expiry.UTC().Format(time.RFC3339)))
		case g.Expiry != nil && g.Expiry.Before(soon):
			warnings = append(warnings, fmt.Sprintf("the authz grant for %s expires at %s", url, g.Expiry.UTC().Format(time.RFC3339)))
		}
	}

	if allowance == nil {
		return append(problems, "no fee allowance from the granter"), warnings
	}

	if allowed, ok := allowance.(*feegrant.AllowedMsgAllowance); ok {
		if !slices.Contains(allowed.AllowedMessages, ExecMsgTypeURL) {
			problems = append(problems, "the fee allowance does not cover "+ExecMsgTypeURL)
		}
		inner, err := allowed.GetAllowance()
		if err != nil {
			return append(problems, "the fee allowance cannot be read: "+err.Error()), warnings
		}
		allowance = inner
	}

	if expiry, err := allowance.ExpiresAt(); err == nil && expiry != nil {
		switch {
		case !expiry.After(now):
			problems = append(problems, "the fee allowance expired at "+expiry.UTC().Format(time.RFC3339))
		case expiry.Before(soon):
			warnings = append(warnings, "the fee allowance expires at "+expiry.UTC().Format(time.RFC3339))
		}
	}

	var limit sdk.Coins
	switch a := allowance.(type) {
	case *feegrant.BasicAllowance:
		limit = a.SpendLimit
	case *feegrant.PeriodicAllowance:
		limit = a.Basic.SpendLimit
	}
	if limit != nil {
		for _, coin := range minSpend {
			if left := limit.AmountOf(coin.Denom); left.LT(coin.Amount) {
				msg := fmt.Sprintf("the fee allowance has %s%s left, less than %s of fees", left, coin.Denom,
					GrantWarningWindow)
				if left.IsZero() {
					problems = append(problems, msg)
				} else {
					warnings = append(warnings, msg)
				}
			}
		}
	}

	return problems, warnings
}

// CheckGrants reads the hot key's grants from the chain and evaluates them.
func (c *Client) CheckGrants(now time.Time, minSpend sdk.Coins) (problems, warnings []string, err error) {
	grants := map[string]GrantState{}
	for _, url := range NodeMsgTypeURLs {
		found, expiry, err := c.QueryGrantExpiry(c.granter, c.FromAddress(), url)
		if err != nil {
			return nil, nil, err
		}
		grants[url] = GrantState{Found: found, Expiry: expiry}
	}

	allowance, err := c.QueryFeeAllowance(c.granter, c.FromAddress())
	if err != nil {
		return nil, nil, err
	}

	problems, warnings = EvaluateGrants(now, grants, allowance, minSpend)

	return problems, warnings, nil
}
