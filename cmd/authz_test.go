// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// Rules: [CH-6].
func TestAuthzCommands(t *testing.T) {
	var buf bytes.Buffer
	err := authzGrants{
		Granter:    "sent1granter",
		Grantee:    "sent1hotkey",
		ChainID:    "sentinelhub-2",
		Node:       "https://rpc.example:443",
		GasPrices:  "0.1udvpn",
		Expiry:     time.Date(2027, 10, 2, 0, 0, 0, 0, time.UTC),
		SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("udvpn", 100_000_000)),
	}.write(&buf)
	if err != nil {
		t.Fatal(err)
	}

	var lines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "sentinelhub ") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 5 {
		t.Fatalf("want four authz grants and a fee grant:\n%s", buf.String())
	}
	common := " --from sent1granter --chain-id sentinelhub-2 --node https://rpc.example:443 --gas auto --gas-adjustment 1.3 --gas-prices 0.1udvpn"
	if lines[0] != "sentinelhub tx authz grant sent1hotkey generic --msg-type /sentinel.node.v3.MsgRegisterNodeRequest --expiration 1822435200"+common {
		t.Fatalf("authz line: %s", lines[0])
	}
	if !strings.Contains(lines[3], "/sentinel.session.v3.MsgUpdateSessionRequest") {
		t.Fatalf("session grant: %s", lines[3])
	}
	if lines[4] != "sentinelhub tx feegrant grant sent1granter sent1hotkey --spend-limit 100000000udvpn --expiration 2027-10-02T00:00:00Z --allowed-messages /cosmos.authz.v1beta1.MsgExec"+common {
		t.Fatalf("feegrant line: %s", lines[4])
	}
}

// TestExpectedFees: the default node, at 0.1udvpn for 210000 gas a
// transaction, sends a status update every 55 minutes and a session report
// every 115 minutes; over 14 days that is 366 + 175 transactions.
//
// Rules: [CH-7].
func TestExpectedFees(t *testing.T) {
	config := types.NewConfig().WithDefaultValues()
	got := expectedFees(config, 14*24*time.Hour)
	if want := sdk.NewCoins(sdk.NewInt64Coin("udvpn", 541*21000)); !got.IsEqual(want) {
		t.Fatalf("expected fees %s, want %s", got, want)
	}
}

// TestExpectedFeesForAnyGasLimit: the gas limit is a uint64 and the estimate
// read it as an int64, so a limit of 1<<63 or more was a negative amount,
// which sdk.NewCoin refuses with a panic.
func TestExpectedFeesForAnyGasLimit(t *testing.T) {
	config := types.NewConfig().WithDefaultValues()
	config.Chain.Gas = 1 << 63
	got := expectedFees(config, time.Hour)
	if !got.IsAllPositive() {
		t.Fatalf("expected fees %s at gas 1<<63, want a positive amount", got)
	}
}
