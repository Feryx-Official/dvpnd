// SPDX-License-Identifier: Apache-2.0

package types

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestDefaultsValidateAndRoundTrip(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if c.VLESS.Security != SecurityTLS || !c.VLESS.Flow || c.Reality.PrivateKey == "" || c.Reality.ShortID == "" {
		t.Fatalf("defaults: %+v %+v", c.VLESS, c.Reality)
	}

	c.VLESS.Security = SecurityReality
	if err := c.Validate(); err != nil {
		t.Fatalf("reality with generated keys must validate: %v", err)
	}

	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := c.SaveToPath(path); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	v.SetConfigFile(path)
	read, err := ReadInConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if read.String() != c.String() {
		t.Fatalf("round trip differs:\n%s\n---\n%s", read.String(), c.String())
	}
	if read.Reality.Dest() != "www.apple.com:443" {
		t.Fatalf("dest: %s", read.Reality.Dest())
	}
}

func TestRealityValidation(t *testing.T) {
	base := func() *Config {
		c := NewConfig().WithDefaultValues()
		c.VLESS.Security = SecurityReality
		return c
	}

	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"mismatched keys", func(c *Config) { c.Reality.PublicKey = c.Reality.PrivateKey }, "does not match"},
		{"bad private key", func(c *Config) { c.Reality.PrivateKey = "nope" }, "private_key must be"},
		{"server name with port", func(c *Config) { c.Reality.ServerName = "a.example:443" }, "without port"},
		{"empty server name", func(c *Config) { c.Reality.ServerName = " " }, "server_name cannot be empty"},
		{"long short id", func(c *Config) { c.Reality.ShortID = strings.Repeat("ab", 9) }, "short_id"},
		{"odd short id", func(c *Config) { c.Reality.ShortID = "abc" }, "short_id"},
		{"non-hex short id", func(c *Config) { c.Reality.ShortID = "zz" }, "short_id must be hex"},
		{"bad security", func(c *Config) { c.VLESS.Security = "none" }, "security must be"},
		{"zero api port", func(c *Config) { c.API.Port = 0 }, "port cannot be zero"},
	}
	for _, tc := range cases {
		c := base()
		tc.edit(c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}

	// With TLS the reality section is not checked at all.
	c := base()
	c.VLESS.Security = SecurityTLS
	c.Reality.PrivateKey = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("tls must ignore the reality keys: %v", err)
	}
}

// TestAPIPortIsNotTheListenPort: the control API and the VLESS inbound both
// listen on TCP, so one port cannot carry both; a file that gives them the
// same port stops the start rather than letting xray fail to bind.
//
// Rules: [RT-23].
func TestAPIPortIsNotTheListenPort(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	c.API.Port = c.VLESS.ListenPort
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "listen_port") {
		t.Fatalf("api port equal to listen_port: %v", err)
	}
}
