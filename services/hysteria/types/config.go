// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/utils"
)

var (
	ct = strings.TrimSpace(`
[server]
# UDP port to accept the incoming connections (QUIC)
listen_port = {{ .Server.ListenPort }}

# Salamander obfuscation password handed to clients; empty disables obfuscation
obfs_password = {{ toml .Server.ObfsPassword }}

# Bandwidth offered to each client, e.g. "100 mbps"; empty lets the client choose
up = {{ toml .Server.Up }}
down = {{ toml .Server.Down }}

[api]
# Loopback ports: hysteria calls auth_port to authenticate a client, and answers
# traffic queries on stats_port; nothing else may bind them
auth_port = {{ .API.AuthPort }}
stats_port = {{ .API.StatsPort }}
	`)

	t = utils.ConfigTemplate("hysteria_toml", ct)

	// bandwidth is what hysteria accepts: a number with an optional unit.
	bandwidth = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?\s*(bps|kbps|mbps|gbps|tbps)?$`)
)

type ServerConfig struct {
	ListenPort   uint16 `json:"listen_port" mapstructure:"listen_port"`
	ObfsPassword string `json:"obfs_password" mapstructure:"obfs_password"`
	Up           string `json:"up" mapstructure:"up"`
	Down         string `json:"down" mapstructure:"down"`
}

func (c *ServerConfig) Validate() error {
	if c.ListenPort == 0 {
		return errors.New("listen_port cannot be zero")
	}
	if strings.ContainsAny(c.ObfsPassword, "\"\n") {
		return errors.New("obfs_password cannot contain quotes or newlines")
	}
	for name, v := range map[string]string{"up": c.Up, "down": c.Down} {
		if v != "" && !bandwidth.MatchString(strings.ToLower(strings.TrimSpace(v))) {
			return errors.Errorf("%s must be a rate such as \"100 mbps\"", name)
		}
	}
	if (c.Up == "") != (c.Down == "") {
		return errors.New("up and down must be set together")
	}

	return nil
}

type APIConfig struct {
	AuthPort  uint16 `json:"auth_port" mapstructure:"auth_port"`
	StatsPort uint16 `json:"stats_port" mapstructure:"stats_port"`
}

func (c *APIConfig) Validate() error {
	if c.AuthPort == 0 || c.StatsPort == 0 {
		return errors.New("auth_port and stats_port cannot be zero")
	}
	if c.AuthPort == c.StatsPort {
		return errors.New("auth_port and stats_port must differ")
	}

	return nil
}

type Config struct {
	Server *ServerConfig `json:"server" mapstructure:"server"`
	API    *APIConfig    `json:"api" mapstructure:"api"`
}

func NewConfig() *Config {
	return &Config{Server: &ServerConfig{}, API: &APIConfig{}}
}

func (c *Config) Validate() error {
	if err := c.Server.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section server")
	}
	if err := c.API.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section api")
	}

	return nil
}

func (c *Config) WithDefaultValues() *Config {
	c.Server.ListenPort = utils.RandomPort()
	c.API.AuthPort = utils.RandomPort()
	c.API.StatsPort = utils.RandomPort()
	for c.API.StatsPort == c.API.AuthPort {
		c.API.StatsPort = utils.RandomPort()
	}

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

// ReadInConfig starts from an empty configuration, so a file without one of
// its ports fails validation instead of getting a random one at every start.
func ReadInConfig(v *viper.Viper) (*Config, error) {
	config := NewConfig()
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	if err := v.Unmarshal(config); err != nil {
		return nil, err
	}

	return config, nil
}
