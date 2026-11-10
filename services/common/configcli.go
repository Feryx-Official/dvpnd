// SPDX-License-Identifier: Apache-2.0

package common

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// ConfigFile is what a protocol's configuration type must offer to be
// managed by ConfigCommand.
type ConfigFile interface {
	SaveToPath(path string) error
	String() string
}

// ConfigSpec describes a protocol's configuration file for ConfigCommand.
type ConfigSpec struct {
	// FileName is the file under the node's home directory.
	FileName string
	// Default returns a configuration with default values.
	Default func() ConfigFile
	// Read loads the configuration from the viper instance, which already
	// points at the file; the value returned must be a pointer so that
	// "config set" can unmarshal into it.
	Read func(v *viper.Viper) (ConfigFile, error)
}

// ConfigCommand builds the "<use> config init|show|set" subtree every
// protocol offers, so a new protocol only describes its file.
func ConfigCommand(use string, aliases []string, short string, spec ConfigSpec) *cobra.Command {
	root := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   short,
	}

	config := &cobra.Command{
		Use:   "config",
		Short: "Configuration sub-commands",
	}
	config.AddCommand(configInit(spec), configShow(spec), configSet(spec))
	root.AddCommand(config)

	return root
}

func configPath(spec ConfigSpec) (home, path string) {
	home = viper.GetString(flags.FlagHome)

	return home, filepath.Join(home, spec.FileName)
}

func configInit(spec ConfigSpec) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Init the configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, path := configPath(spec)

			force, err := cmd.Flags().GetBool(types.FlagForce)
			if err != nil {
				return err
			}

			if !force {
				if _, err = os.Stat(path); err == nil {
					return fmt.Errorf("config file already exists at path %s", path)
				}
			}

			if err = os.MkdirAll(home, 0700); err != nil {
				return err
			}

			return spec.Default().SaveToPath(path)
		},
	}

	cmd.Flags().Bool(types.FlagForce, false, "force")

	return cmd
}

func configShow(spec ConfigSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the configuration",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, path := configPath(spec)

			v := viper.New()
			v.SetConfigFile(path)

			config, err := spec.Read(v)
			if err != nil {
				return err
			}

			fmt.Println(config.String())

			return nil
		},
	}
}

func configSet(spec ConfigSpec) *cobra.Command {
	return &cobra.Command{
		Use:   "set [key] [value]",
		Short: "Set the configuration",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			_, path := configPath(spec)

			v := viper.New()
			v.SetConfigFile(path)

			config, err := spec.Read(v)
			if err != nil {
				return err
			}

			if err = types.SetConfigKey(v, config, args[0], args[1]); err != nil {
				return err
			}

			return config.SaveToPath(path)
		},
	}
}
