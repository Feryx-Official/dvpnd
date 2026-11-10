// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	base "github.com/sentinel-official/sentinelhub/v12/types"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

const (
	flagGranter    = "granter"
	flagValidFor   = "valid-for"
	flagSpendLimit = "spend-limit"
)

// authzGrants is what the operator grants the hot key from the node
// account's wallet.
type authzGrants struct {
	Granter, Grantee string
	ChainID, Node    string
	GasPrices        string
	Expiry           time.Time
	SpendLimit       sdk.Coins
}

// write prints the sentinelhub commands that make the grants: one authz
// grant per message the node sends, and a fee allowance limited to MsgExec.
// authz takes its expiry as a Unix time, feegrant as RFC 3339.
func (g authzGrants) write(w io.Writer) error {
	common := fmt.Sprintf("--from %s --chain-id %s --node %s --gas auto --gas-adjustment 1.3 --gas-prices %s",
		g.Granter, g.ChainID, g.Node, g.GasPrices)

	var b strings.Builder
	fmt.Fprintf(&b, "# Run these with the sentinelhub CLI from the wallet that holds the node account\n")
	fmt.Fprintf(&b, "# %s, not on this server. They let the hot key %s send the\n", g.Granter, g.Grantee)
	fmt.Fprintf(&b, "# node's messages and spend up to %s of the node account's funds on their\n", g.SpendLimit)
	fmt.Fprintf(&b, "# fees, for MsgExec only, until %s. Make them again before then.\n", g.Expiry.UTC().Format(time.RFC3339))
	if granter, err := sdk.AccAddressFromBech32(g.Granter); err == nil {
		fmt.Fprintf(&b, "# Node address: %s\n", base.NodeAddress(granter.Bytes()))
	}
	fmt.Fprintf(&b, "\n")
	for _, url := range lite.NodeMsgTypeURLs {
		fmt.Fprintf(&b, "sentinelhub tx authz grant %s generic --msg-type %s --expiration %d %s\n",
			g.Grantee, url, g.Expiry.Unix(), common)
	}
	fmt.Fprintf(&b, "sentinelhub tx feegrant grant %s %s --spend-limit %s --expiration %s --allowed-messages %s %s\n",
		g.Granter, g.Grantee, g.SpendLimit, g.Expiry.UTC().Format(time.RFC3339), lite.ExecMsgTypeURL, common)

	_, err := io.WriteString(w, b.String())

	return err
}

func keysAuthzCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "authz-commands",
		Short: "Print the grants that let the signing key act as a hot key for the node account",
		Long: `Print the sentinelhub commands that grant the signing key ([keyring] from) the right
to send the node's messages for the node account ([keyring] granter, or --granter) and to
have their fees paid from it. Run them from the wallet that holds the node account; the
node then signs with the hot key alone, and the operator key stays off the server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := viper.GetString(flags.FlagHome)

			v := viper.New()
			v.SetConfigFile(filepath.Join(home, types.ConfigFileName))
			config, err := types.ReadInConfig(v)
			if err != nil {
				return err
			}

			granter, err := cmd.Flags().GetString(flagGranter)
			if err != nil {
				return err
			}
			if granter == "" {
				granter = config.Keyring.Granter
			}
			if _, err = sdk.AccAddressFromBech32(granter); err != nil {
				return fmt.Errorf("the node account: set [keyring] granter or pass --granter sent1...: %w", err)
			}

			validFor, err := cmd.Flags().GetDuration(flagValidFor)
			if err != nil {
				return err
			}
			limit, err := cmd.Flags().GetString(flagSpendLimit)
			if err != nil {
				return err
			}
			spendLimit, err := sdk.ParseCoinsNormalized(limit)
			if err != nil || spendLimit.Empty() {
				return fmt.Errorf("invalid --%s %q", flagSpendLimit, limit)
			}

			kr, err := keyring.New(types.KeyringName, config.Keyring.Backend, home,
				bufio.NewReader(cmd.InOrStdin()), lite.DefaultEncodingConfig().Codec)
			if err != nil {
				return err
			}
			key, err := kr.Key(config.Keyring.From)
			if err != nil {
				return err
			}
			grantee, err := key.GetAddress()
			if err != nil {
				return err
			}
			if grantee.String() == granter {
				return fmt.Errorf("the key %q is the node account itself; create a separate hot key", config.Keyring.From)
			}

			node, _, _ := strings.Cut(config.Chain.RPCAddresses, ",")

			return authzGrants{
				Granter:    granter,
				Grantee:    grantee.String(),
				ChainID:    config.Chain.ID,
				Node:       node,
				GasPrices:  config.Chain.GasPrices,
				Expiry:     time.Now().Add(validFor),
				SpendLimit: spendLimit,
			}.write(cmd.OutOrStdout())
		},
	}

	cmd.Flags().String(flagGranter, "", "node account (sent1...); default [keyring] granter")
	cmd.Flags().Duration(flagValidFor, 365*24*time.Hour, "how long the grants last")
	cmd.Flags().String(flagSpendLimit, "100000000udvpn", "most the hot key may spend on fees")

	return cmd
}
