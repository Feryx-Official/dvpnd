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

	// LegacyKeyringName is the keyring service name the upstream node used. It is
	// only consulted to point operators at keys stored under the old name.
	LegacyKeyringName = "sentinel"
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

	// LegacyHomeDirectory is where the upstream node kept its configuration,
	// database and file keyring. Nothing is read from or written to it; see
	// LegacyHomeHint.
	LegacyHomeDirectory = func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}

		return filepath.Join(home, ".sentinelnode")
	}()
)

// LegacyHomeHint returns a message for the operator when home is the default
// home directory, it does not exist yet, and the upstream node's directory
// does. It never moves or modifies anything: migrating keys and the session
// database is a deliberate step the operator takes, not a side effect of
// starting a new version.
func LegacyHomeHint(home string) string {
	if home != DefaultHomeDirectory {
		return ""
	}
	if _, err := os.Stat(home); err == nil {
		return ""
	}
	if _, err := os.Stat(LegacyHomeDirectory); err != nil {
		return ""
	}

	return "home directory " + home + " does not exist but " + LegacyHomeDirectory +
		" (upstream node) does; copy it to " + home + " to reuse its config.toml, data.db and " +
		"file keyring, or pass --home " + LegacyHomeDirectory + ". Keys kept in the OS keyring " +
		"were stored under the service name \"" + LegacyKeyringName + "\" and must be re-imported."
}
