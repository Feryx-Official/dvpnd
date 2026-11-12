// SPDX-License-Identifier: Apache-2.0

package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/amneziawg"
	awgtypes "github.com/Feryx-Official/dvpnd/v9/services/amneziawg/types"
	"github.com/Feryx-Official/dvpnd/v9/services/hysteria"
	hysteriatypes "github.com/Feryx-Official/dvpnd/v9/services/hysteria/types"
	"github.com/Feryx-Official/dvpnd/v9/services/openvpn"
	ovpntypes "github.com/Feryx-Official/dvpnd/v9/services/openvpn/types"
	"github.com/Feryx-Official/dvpnd/v9/services/v2ray"
	v2raytypes "github.com/Feryx-Official/dvpnd/v9/services/v2ray/types"
	"github.com/Feryx-Official/dvpnd/v9/services/wireguard"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/services/xray"
	xraytypes "github.com/Feryx-Official/dvpnd/v9/services/xray/types"
)

// protocolFile is what this test needs of a protocol's configuration.
type protocolFile interface {
	String() string
	SaveToPath(path string) error
	Validate() error
}

// fileCase is one protocol's configuration file: what "config init" writes,
// how the node reads it, and the keys a read may still draw at random.
type fileCase struct {
	defaults func() protocolFile
	read     func(v *viper.Viper) (protocolFile, error)
	drawn    map[string]bool
}

func fileCases() map[string]fileCase {
	return map[string]fileCase{
		wireguard.Name: {
			defaults: func() protocolFile { return wgtypes.NewConfig().WithDefaultValues() },
			read:     func(v *viper.Viper) (protocolFile, error) { return wgtypes.ReadInConfig(v) },
		},
		v2ray.Name: {
			defaults: func() protocolFile { return v2raytypes.NewConfig().WithDefaultValues() },
			read:     func(v *viper.Viper) (protocolFile, error) { return v2raytypes.ReadInConfig(v) },
			// Files from before [api] existed lack it (RT-9).
			drawn: map[string]bool{"api.port": true},
		},
		xray.Name: {
			defaults: func() protocolFile { return xraytypes.NewConfig().WithDefaultValues() },
			read:     func(v *viper.Viper) (protocolFile, error) { return xraytypes.ReadInConfig(v) },
		},
		hysteria.Name: {
			defaults: func() protocolFile { return hysteriatypes.NewConfig().WithDefaultValues() },
			read:     func(v *viper.Viper) (protocolFile, error) { return hysteriatypes.ReadInConfig(v) },
		},
		amneziawg.Name: {
			defaults: func() protocolFile { return awgtypes.NewConfig().WithDefaultValues() },
			read:     func(v *viper.Viper) (protocolFile, error) { return awgtypes.ReadInConfig(v) },
		},
		openvpn.Name: {
			defaults: func() protocolFile { return ovpntypes.NewConfig().WithDefaultValues() },
			read:     func(v *viper.Viper) (protocolFile, error) { return ovpntypes.ReadInConfig(v) },
		},
	}
}

// TestAMissingKeyIsNotDrawnAtRead: a protocol's file read without one of
// its keys reads the same every time. A read that filled a missing port or
// key at random gave the node a new listen port, or a new WireGuard key, at
// every start, while clients held the old one. A missing listen port, or a
// tunnel's missing private key, stops the start, naming the key.
//
// Rules: [RT-23].
func TestAMissingKeyIsNotDrawnAtRead(t *testing.T) {
	cases := fileCases()
	for _, p := range All() {
		fc, ok := cases[p.Name]
		if !ok {
			t.Errorf("%s has no case here", p.Name)
			continue
		}
		lines := strings.Split(fc.defaults().String(), "\n")
		section := ""
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				section = strings.Trim(trimmed, "[]") + "."
				continue
			}
			key, _, isKey := strings.Cut(trimmed, " = ")
			if !isKey || strings.HasPrefix(trimmed, "#") {
				continue
			}
			id := section + key

			path := filepath.Join(t.TempDir(), "file.toml")
			without := append(append([]string{}, lines[:i]...), lines[i+1:]...)
			if err := os.WriteFile(path, []byte(strings.Join(without, "\n")), 0o600); err != nil {
				t.Fatal(err)
			}
			var reads [2]protocolFile
			for n := range reads {
				v := viper.New()
				v.SetConfigFile(path)
				var err error
				if reads[n], err = fc.read(v); err != nil {
					t.Fatalf("%s without %s: %v", p.Name, id, err)
				}
			}
			if !fc.drawn[id] && reads[0].String() != reads[1].String() {
				t.Errorf("%s without %s: two reads differ, so the read draws it", p.Name, id)
			}
			if key == "listen_port" || id == "private_key" {
				if err := reads[0].Validate(); err == nil || !strings.Contains(err.Error(), key) {
					t.Errorf("%s without %s: validation says %v", p.Name, id, err)
				}
			}
		}
	}
}
