// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

import (
	"bytes"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/utils"
)

var (
	ct = strings.TrimSpace(`
# Name of the network interface
interface = {{ toml .Interface }}

# Port number to accept the incoming connections
listen_port = {{ .ListenPort }}

# Server private key
private_key = {{ toml .PrivateKey }}

# Network interface that carries the node's internet traffic; peers are NAT-ed
# through it. Empty means detect it from the default route at start
uplink = {{ toml .Uplink }}

# Hand each peer an IPv6 tunnel address next to the IPv4 one (true, as every
# other node on the network does). It needs a host that can reach the IPv6
# internet; in Docker that means "ipv6": true in the daemon config, otherwise
# clients get "unreachable" on every IPv6 connection through the tunnel. Set it
# false for an IPv4-only tunnel: clients then exit with the node's IPv4 address.
enable_ipv6 = {{ .EnableIPv6 }}
	`)

	t = utils.ConfigTemplate("wireguard_toml", ct)
)

type Config struct {
	Interface  string `json:"interface" mapstructure:"interface"`
	ListenPort uint16 `json:"listen_port" mapstructure:"listen_port"`
	PrivateKey string `json:"private_key" mapstructure:"private_key"`
	Uplink     string `json:"uplink" mapstructure:"uplink"`
	EnableIPv6 bool   `json:"enable_ipv6" mapstructure:"enable_ipv6"`
}

func NewConfig() *Config {
	return &Config{}
}

func (c *Config) Validate() error {
	if err := common.CheckInterfaceName("interface", c.Interface); err != nil {
		return err
	}
	if c.Uplink != "" {
		if err := common.CheckInterfaceName("uplink", c.Uplink); err != nil {
			return err
		}
	}
	if c.ListenPort == 0 {
		return errors.New("listen_port cannot be zero")
	}
	if c.PrivateKey == "" {
		return errors.New("private_key cannot be empty")
	}
	if _, err := KeyFromString(c.PrivateKey); err != nil {
		return errors.Wrap(err, "invalid private_key")
	}

	return nil
}

// withStaticDefaults fills the values that are the same for every node; it
// is what a file read from disk starts from, so a file without its port or
// key fails validation instead of getting a fresh one at every start.
func (c *Config) withStaticDefaults() *Config {
	c.Interface = "wg0"
	c.EnableIPv6 = true

	return c
}

// WithDefaultValues is what "config init" writes: the static defaults, a
// random port and a fresh key.
func (c *Config) WithDefaultValues() *Config {
	key, err := NewPrivateKey()
	if err != nil {
		panic(err)
	}

	c.withStaticDefaults()
	c.ListenPort = utils.RandomPort()
	c.PrivateKey = key.String()

	return c
}

func (c *Config) SaveToPath(path string) error {
	var buffer bytes.Buffer
	if err := t.Execute(&buffer, c); err != nil {
		return err
	}

	return utils.WritePrivateFile(path, buffer.Bytes())
}

func (c *Config) String() string {
	var buffer bytes.Buffer
	if err := t.Execute(&buffer, c); err != nil {
		panic(err)
	}

	return buffer.String()
}

func ReadInConfig(v *viper.Viper) (*Config, error) {
	config := NewConfig().withStaticDefaults()
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	if err := v.Unmarshal(config); err != nil {
		return nil, err
	}

	return config, nil
}
