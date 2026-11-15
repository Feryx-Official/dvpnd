// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

import (
	"os"
	"path/filepath"
)

const (
	ConfigFileName   = "config.toml"
	DatabaseFileName = "data.db"
	IPv4CIDR         = "10.8.0.2/24"
	IPv6CIDR         = "fd86:ea04:1115::2/120"
	// AppName is what this node software calls itself wherever it names the
	// implementation: the root document's version, the Server header.
	AppName     = "dvpnd"
	KeyringName = "dvpnd"
)

const (
	FlagForce = "force"
)

var (
	DefaultHomeDirectory = func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}

		return filepath.Join(home, ".dvpnd")
	}()
)
