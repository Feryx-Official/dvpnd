// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestConfig(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	path := filepath.Join(t.TempDir(), ConfigFileName)
	c.Proto = ProtoTCP
	if err := c.SaveToPath(path); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	v.SetConfigFile(path)
	read, err := ReadInConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if read.String() != c.String() || read.Proto != ProtoTCP || read.Interface != "ovpn0" {
		t.Fatalf("round trip:\n%s", read.String())
	}

	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"proto", func(c *Config) { c.Proto = "sctp" }, "proto must be"},
		{"port", func(c *Config) { c.ListenPort = 0 }, "listen_port"},
		{"interface", func(c *Config) { c.Interface = "a b" }, "interface"},
	}
	for _, tc := range cases {
		c := NewConfig().WithDefaultValues()
		tc.edit(c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
}

// TestConfigWithManagementPort: a file written when the management interface
// was a loopback port still reads; the port is ignored.
//
// Rules: [RT-9].
func TestConfigWithManagementPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	c := NewConfig().WithDefaultValues()
	old := c.String() + "\n[management]\nport = 41000\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	v.SetConfigFile(path)
	read, err := ReadInConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Validate(); err != nil || read.ListenPort != c.ListenPort {
		t.Fatalf("read %+v: %v", read, err)
	}
}

// TestConfigRefusesUnsafeNames: the interface and uplink names reach
// iptables and the server's configuration.
//
// Rules: [RT-17].
func TestConfigRefusesUnsafeNames(t *testing.T) {
	for _, name := range []string{"ovpn0;id", "$(id)", "`id`", "a/b", "ovpn-123456789ab"} {
		c := NewConfig().WithDefaultValues()
		c.Interface = name
		if err := c.Validate(); err == nil {
			t.Errorf("interface %q accepted", name)
		}

		c = NewConfig().WithDefaultValues()
		c.Uplink = name
		if err := c.Validate(); err == nil {
			t.Errorf("uplink %q accepted", name)
		}
	}
}
