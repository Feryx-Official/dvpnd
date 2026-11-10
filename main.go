// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package main

import (
	"os"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/version"
	base "github.com/sentinel-official/sentinelhub/v12/types"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/cmd"
	"github.com/Feryx-Official/dvpnd/v9/services"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

func main() {
	base.GetConfig().Seal()
	root := &cobra.Command{
		Use:          "dvpnd",
		SilenceUsage: true,
	}

	root.AddCommand(
		cmd.ConfigCmd(),
		cmd.KeysCmd(),
		cmd.StartCmd(),
		version.NewVersionCommand(),
	)
	for _, p := range services.All() {
		root.AddCommand(p.Command())
	}

	root.PersistentFlags().String(flags.FlagHome, types.DefaultHomeDirectory, "home directory")
	root.PersistentFlags().String(flags.FlagLogFormat, "plain", "log format")
	root.PersistentFlags().String(flags.FlagLogLevel, "info", "log level")

	_ = viper.BindPFlag(flags.FlagHome, root.PersistentFlags().Lookup(flags.FlagHome))
	_ = viper.BindPFlag(flags.FlagLogFormat, root.PersistentFlags().Lookup(flags.FlagLogFormat))
	_ = viper.BindPFlag(flags.FlagLogLevel, root.PersistentFlags().Lookup(flags.FlagLogLevel))

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
