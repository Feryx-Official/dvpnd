// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/utils"
)

var (
	ct = strings.TrimSpace(`
[vmess]
# Port number to accept the incoming connections
listen_port = {{ .VMess.ListenPort }}

# Enable or disable TLS for secure connections
tls = {{ .VMess.TLS }}

# Transport protocol for the VMess inbound; "tcp" is the only one served
transport = {{ toml .VMess.Transport }}

[api]
# Loopback port on which the node drives v2ray; nothing else may bind it
port = {{ .API.Port }}
	`)

	t = utils.ConfigTemplate("v2ray_toml", ct)
)

// VMessConfig is the [vmess] section. Security and the TLS paths are the
// node's own, set at Init from tls; no key of the file reaches them.
type VMessConfig struct {
	Security    string `json:"security" mapstructure:"-"`
	TLSCertPath string `json:"tls_cert_path" mapstructure:"-"`
	TLSKeyPath  string `json:"tls_key_path" mapstructure:"-"`

	ListenPort uint16 `json:"listen_port" mapstructure:"listen_port"`
	TLS        bool   `json:"tls" mapstructure:"tls"`
	Transport  string `json:"transport" mapstructure:"transport"`
}

func NewVMessConfig() *VMessConfig {
	return &VMessConfig{}
}

func (c *VMessConfig) WithDefaultValues() *VMessConfig {
	c.ListenPort = utils.RandomPort()
	c.TLS = false
	c.Transport = "tcp"

	return c
}

func (c *VMessConfig) Validate() error {
	if c.ListenPort == 0 {
		return errors.New("listen_port cannot be zero")
	}
	if !NewTransportFromString(c.Transport).IsValid() {
		return fmt.Errorf("transport must be %q, the only one served; got %q", "tcp", c.Transport)
	}

	return nil
}

// APIConfig is where the node reaches v2ray's control API. A file written
// before the section existed gets a fresh random port at every start, which
// works because the node renders v2ray's configuration from the same value.
type APIConfig struct {
	Port uint16 `json:"port" mapstructure:"port"`
}

func NewAPIConfig() *APIConfig {
	return &APIConfig{}
}

func (c *APIConfig) WithDefaultValues() *APIConfig {
	c.Port = utils.RandomPort()

	return c
}

func (c *APIConfig) Validate() error {
	if c.Port == 0 {
		return errors.New("port cannot be zero")
	}

	return nil
}

type Config struct {
	VMess *VMessConfig `json:"vmess" mapstructure:"vmess"`
	API   *APIConfig   `json:"api" mapstructure:"api"`
}

func NewConfig() *Config {
	return &Config{
		VMess: NewVMessConfig(),
		API:   NewAPIConfig(),
	}
}

func (c *Config) Validate() error {
	if err := c.VMess.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section vmess")
	}
	if err := c.API.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section api")
	}
	if c.API.Port == c.VMess.ListenPort {
		return errors.New("api port must differ from the vmess listen_port")
	}

	return nil
}

func (c *Config) WithDefaultValues() *Config {
	c.VMess = c.VMess.WithDefaultValues()
	c.API = c.API.WithDefaultValues()

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
	config := NewConfig().WithDefaultValues()
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	if err := v.Unmarshal(config); err != nil {
		return nil, err
	}

	return config, nil
}
