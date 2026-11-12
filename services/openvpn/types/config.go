// SPDX-License-Identifier: Apache-2.0

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
# Name of the tunnel interface
interface = {{ toml .Interface }}

# Port number to accept the incoming connections
listen_port = {{ .ListenPort }}

# Transport: "udp" (recommended) or "tcp"
proto = {{ toml .Proto }}

# Network interface that carries the node's internet traffic; peers are NAT-ed
# through it. Empty means detect it from the default route at start
uplink = {{ toml .Uplink }}

# Hand each peer an IPv6 tunnel address next to the IPv4 one; the host must
# then reach the IPv6 internet. Set it false for an IPv4-only tunnel
enable_ipv6 = {{ .EnableIPv6 }}
	`)

	t = utils.ConfigTemplate("openvpn_toml", ct)
)

// Config is openvpn.toml. A file written before the management interface
// moved to a unix socket still has a [management] port; it is ignored.
type Config struct {
	Interface  string `json:"interface" mapstructure:"interface"`
	ListenPort uint16 `json:"listen_port" mapstructure:"listen_port"`
	Proto      string `json:"proto" mapstructure:"proto"`
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
	if c.ListenPort == 0 {
		return errors.New("listen_port cannot be zero")
	}
	if c.Proto != ProtoUDP && c.Proto != ProtoTCP {
		return errors.Errorf("proto must be %q or %q", ProtoUDP, ProtoTCP)
	}
	if c.Uplink != "" {
		if err := common.CheckInterfaceName("uplink", c.Uplink); err != nil {
			return err
		}
	}

	return nil
}

// withStaticDefaults fills the values that are the same for every node; it
// is what a file read from disk starts from, so a file without its port
// fails validation instead of getting a random one at every start.
func (c *Config) withStaticDefaults() *Config {
	c.Interface = "ovpn0"
	c.Proto = ProtoUDP
	c.EnableIPv6 = true

	return c
}

func (c *Config) WithDefaultValues() *Config {
	c.withStaticDefaults()
	c.ListenPort = utils.RandomPort()

	return c
}

func (c *Config) SaveToPath(path string) error {
	var buf bytes.Buffer
	if err := t.Execute(&buf, c); err != nil {
		return err
	}

	return utils.WritePrivateFile(path, buf.Bytes())
}

func (c *Config) String() string {
	var buf bytes.Buffer
	if err := t.Execute(&buf, c); err != nil {
		panic(err)
	}

	return buf.String()
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
