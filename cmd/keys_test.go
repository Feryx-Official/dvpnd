// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// TestKeysAddShowsOnlyANewMnemonic: a new key's mnemonic is printed once, as
// its only backup; a recovered one is not printed back, so it does not end up
// in the scrollback or in a log of the install.
//
// Rules: [PV-5].
func TestKeysAddShowsOnlyANewMnemonic(t *testing.T) {
	dir := t.TempDir()
	cfg := types.NewConfig().WithDefaultValues()
	cfg.Keyring.Backend = keyring.BackendTest
	if err := cfg.SaveToPath(filepath.Join(dir, types.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	viper.Set(flags.FlagHome, dir)
	t.Cleanup(func() { viper.Set(flags.FlagHome, "") })

	// The BIP-39 test vector for all-zero entropy, not anyone's wallet.
	const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon " +
		"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"

	run := func(args ...string) (string, string) {
		cmd := keysAdd()
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetIn(strings.NewReader(mnemonic + "\n"))
		cmd.SetArgs(append(args, "--"+flagSkipConfigValidation))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String(), errOut.String()
	}

	out, errOut := run("restored", "--"+flagRecover)
	if strings.Contains(out+errOut, mnemonic) || strings.Contains(errOut, "**Important**") {
		t.Fatalf("a recovered mnemonic must not be printed back:\nstdout: %s\nstderr: %s", out, errOut)
	}
	if !strings.Contains(out, "restored") {
		t.Fatalf("the recovered key is listed: %s", out)
	}

	_, errOut = run("fresh")
	if !strings.Contains(errOut, "**Important** write this mnemonic phrase in a safe place") {
		t.Fatalf("a new key's mnemonic must be shown once: %s", errOut)
	}
}
