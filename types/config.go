// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

import (
	"bytes"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/pkg/errors"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/libs/geoip"

	"github.com/Feryx-Official/dvpnd/v9/utils"
)

const (
	MinPeers                  = 1
	MaxPeers                  = 250
	MinMonikerLength          = 4
	MaxMonikerLength          = 32
	MinIntervalSetSessions    = 2 * time.Second
	MaxIntervalSetSessions    = 2 * time.Minute
	MinIntervalUpdateSessions = 10 * time.Second
	MaxIntervalUpdateSessions = (2 * time.Hour) - (5 * time.Minute)
	MinIntervalUpdateStatus   = 10 * time.Second
	MaxIntervalUpdateStatus   = (1 * time.Hour) - (5 * time.Minute)
	// MaxBandwidthMbps bounds a declared link: a terabit is a typo, not a link.
	MaxBandwidthMbps = 1_000_000
)

var (
	ct = strings.TrimSpace(`
[bandwidth]
# Bandwidth this node advertises, in megabits per second.
# Leave both at 0 and the node measures its link: it picks speed test servers by
# measured latency, skips any that answer from inside its own datacenter (they
# measure the local network, not the internet), and reports the lowest of what two
# or three independent servers manage. The measurement takes one to two minutes,
# moves several gigabytes on a fast link, is kept in bandwidth.json and repeated
# once a week or when the public IP changes. It never overstates; on a multi-gigabit
# link it may understate, because public test servers cannot always keep up.
# Set both to what your provider actually sells you (a 1 Gbit/s port is 1000) and
# the measurement is skipped entirely. Nothing verifies the figure and clients use
# it to choose a node, so do not overstate it. Set both or neither.
download_mbps = {{ .Bandwidth.DownloadMbps }}
upload_mbps = {{ .Bandwidth.UploadMbps }}

[chain]
# Gas limit to set per transaction
gas = {{ .Chain.Gas }}

# Gas adjustment factor
gas_adjustment = {{ .Chain.GasAdjustment }}

# Gas prices to determine the transaction fee
gas_prices = {{ toml .Chain.GasPrices }}

# The network chain ID
id = {{ toml .Chain.ID }}

# Comma separated Tendermint RPC addresses for the chain; https, or http on loopback only
rpc_addresses = {{ toml .Chain.RPCAddresses }}

# Timeout seconds for querying the data from the RPC server
rpc_query_timeout = {{ .Chain.RPCQueryTimeout }}

# Timeout seconds for broadcasting the transaction through RPC server
rpc_tx_timeout = {{ .Chain.RPCTxTimeout }}

# Calculate the transaction fee by simulating it
simulate_and_execute = {{ .Chain.SimulateAndExecute }}

[egress]
# Clients never reach the node host itself, private, shared or link-local networks (the
# provider's metadata service among them), or each other. Outgoing mail (TCP port 25) is
# blocked too, because a node that relays anyone's mail ends up on block lists and its
# provider may suspend it. Set to true only if you accept that.
allow_smtp = {{ .Egress.AllowSMTP }}

[geoip]
# Service that discovers the node's public IP and location. All of them are free of
# charge; the free tiers of auto, ipwhois, ip2location and cloudflare allow commercial use.
#   auto        - (default) ipwho.is, then ip2location.io, until one returns a full location;
#                 the country is cross-checked against Cloudflare and a mismatch is logged.
#                 If neither answers: Cloudflare (country only), then ipify (IP only).
#                 Keyless; url and token must stay empty.
#   ipwhois     - ipwho.is: IP, city, country, coordinates. Keyless, 1000 lookups per day.
#   ip2location - ip2location.io: same fields. Keyless 1000 per day; a free key (token) raises it.
#   cloudflare  - IP and country code only.
#   ipify       - IP only.
#   ip-api      - IP, city, country, coordinates. Free tier is for NON-COMMERCIAL use only;
#                 a paid key goes in url.
#   ipinfo      - IP, country (free Lite plan, token required); city and coordinates need a
#                 paid plan (set url to its endpoint).
#   none        - no lookup; node.ipv4_address is used as the public IP.
provider = {{ toml .GeoIP.Provider }}

# Optional endpoint override; only for a single provider, never with auto or none
url = {{ toml .GeoIP.URL }}

# API token: required for ipinfo, optional for ip2location, not accepted otherwise
token = {{ toml .GeoIP.Token }}

# Static location; when set, these override what the provider returns.
# Clients use the reported location to choose a node and nothing verifies it:
# enter the server's real physical location, never an invented one.
city = {{ toml .GeoIP.City }}
country = {{ toml .GeoIP.Country }}
latitude = {{ printf "%.6f" .GeoIP.Latitude }}
longitude = {{ printf "%.6f" .GeoIP.Longitude }}

[handshake]
# Enable Handshake DNS resolver
enable = {{ .Handshake.Enable }}

# Number of peers
peers = {{ .Handshake.Peers }}

[keyring]
# Underlying storage mechanism for keys
backend = {{ toml .Keyring.Backend }}

# Name of the key with which to sign
from = {{ toml .Keyring.From }}

# The node account, when the key above is a hot key: the address (sent1...) that granted
# it the right to send the node's messages (authz) and pays its fees (feegrant). The
# operator key and the earnings then stay off this server. Empty: the key above is the
# node account itself. "dvpnd keys authz-commands" prints the grants to make.
granter = {{ toml .Keyring.Granter }}

[node]
# Time interval between each set_sessions operation
interval_set_sessions = {{ toml .Node.IntervalSetSessions }}

# Time interval between each update_sessions transaction
interval_update_sessions = {{ toml .Node.IntervalUpdateSessions }}

# Time interval between each set_status transaction
interval_update_status = {{ toml .Node.IntervalUpdateStatus }}

# IPv4 address to replace the public IPv4 address with
ipv4_address = {{ toml .Node.IPv4Address }}

# API listen-address
listen_on = {{ toml .Node.ListenOn }}

# Name of the node
moniker = {{ toml .Node.Moniker }}

# Prices for one gigabyte of bandwidth provided. Either plain coins ("1000udvpn,5uatom")
# or the chain's price form "denom:base_value,quote_value" separated by ";"
# (a non-zero base_value lets the chain re-quote the price via its oracle)
gigabyte_prices = {{ toml .Node.GigabytePrices }}

# Prices for one hour, same format
hourly_prices = {{ toml .Node.HourlyPrices }}

# Public URL of the node (https://host:port); the chain records the host:port part
remote_url = {{ toml .Node.RemoteURL }}

# Type of node
type = {{ toml .Node.Type }}

# Also accept handshakes on the legacy endpoint POST /accounts/<address>/sessions/<id>,
# which only old clients use. Its signature covers the session id alone, so a captured
# request can be replayed with another key and cut the real client off. Leave it off
# unless a client you serve still needs it.
legacy_handshake = {{ .Node.LegacyHandshake }}

[qos]
# Limit max number of concurrent peers
max_peers = {{ .QOS.MaxPeers }}
	`)

	t = utils.ConfigTemplate("config_toml", ct)
)

// BandwidthConfig is the operator's declared link. Both zero (the default)
// means the node measures it at start; see libs/bandwidth.
type BandwidthConfig struct {
	DownloadMbps float64 `json:"download_mbps" mapstructure:"download_mbps"`
	UploadMbps   float64 `json:"upload_mbps" mapstructure:"upload_mbps"`
}

func NewBandwidthConfig() *BandwidthConfig {
	return &BandwidthConfig{}
}

func (c *BandwidthConfig) Validate() error {
	for _, f := range []struct {
		name  string
		value float64
	}{
		{"download_mbps", c.DownloadMbps},
		{"upload_mbps", c.UploadMbps},
	} {
		if math.IsNaN(f.value) || math.IsInf(f.value, 0) {
			return fmt.Errorf("%s must be a number", f.name)
		}
		if f.value < 0 {
			return fmt.Errorf("%s cannot be negative", f.name)
		}
		if f.value > MaxBandwidthMbps {
			return fmt.Errorf("%s cannot be greater than %d", f.name, MaxBandwidthMbps)
		}
	}
	if (c.DownloadMbps > 0) != (c.UploadMbps > 0) {
		return errors.New("set both download_mbps and upload_mbps, or neither")
	}

	return nil
}

func (c *BandwidthConfig) WithDefaultValues() *BandwidthConfig {
	return c
}

type ChainConfig struct {
	Gas                uint64  `json:"gas" mapstructure:"gas"`
	GasAdjustment      float64 `json:"gas_adjustment" mapstructure:"gas_adjustment"`
	GasPrices          string  `json:"gas_prices" mapstructure:"gas_prices"`
	ID                 string  `json:"id" mapstructure:"id"`
	RPCAddresses       string  `json:"rpc_addresses" mapstructure:"rpc_addresses"`
	RPCQueryTimeout    uint    `json:"rpc_query_timeout" mapstructure:"rpc_query_timeout"`
	RPCTxTimeout       uint    `json:"rpc_tx_timeout" mapstructure:"rpc_tx_timeout"`
	SimulateAndExecute bool    `json:"simulate_and_execute" mapstructure:"simulate_and_execute"`
}

func NewChainConfig() *ChainConfig {
	return &ChainConfig{}
}

func (c *ChainConfig) Validate() error {
	if c.Gas <= 0 {
		return errors.New("gas must be positive")
	}
	if !(c.GasAdjustment > 0) || math.IsInf(c.GasAdjustment, 1) {
		return errors.New("gas_adjustment must be a positive number")
	}
	if _, err := sdk.ParseCoinsNormalized(c.GasPrices); err != nil {
		return errors.Wrap(err, "invalid gas_prices")
	}
	if c.ID == "" {
		return errors.New("id cannot be empty")
	}
	if c.RPCAddresses == "" {
		return errors.New("rpc_addresses cannot be empty")
	}

	items := strings.Split(c.RPCAddresses, ",")
	for i := 0; i < len(items); i++ {
		uri, err := url.ParseRequestURI(items[i])
		if err != nil {
			return errors.Wrapf(err, "invalid rpc_address %s", items[i])
		}
		if uri.Scheme != "http" && uri.Scheme != "https" {
			return errors.New("rpc_address scheme must be either http or https")
		}
		// The node trusts what the RPC answers (sessions it admits, their
		// status), so on any path that leaves the host it must be TLS.
		if uri.Scheme == "http" && !loopbackHost(uri.Hostname()) {
			return fmt.Errorf("rpc_address %s must use https; plain http is accepted on loopback only", items[i])
		}
		if uri.Port() == "" {
			return errors.New("rpc_address port cannot be empty")
		}
	}

	if c.RPCQueryTimeout == 0 {
		return errors.New("rpc_query_timeout cannot be 0")
	}
	if c.RPCTxTimeout == 0 {
		return errors.New("rpc_tx_timeout cannot be 0")
	}

	return nil
}

// loopbackHost reports whether host names this machine's loopback.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func (c *ChainConfig) WithDefaultValues() *ChainConfig {
	c.Gas = 200_000
	c.GasAdjustment = 1.05
	c.GasPrices = "0.1udvpn"
	c.ID = "sentinelhub-2"
	c.RPCAddresses = "https://sentinel-rpc.publicnode.com:443,https://sentinel-rpc.polkachu.com:443,https://rpc-sentinel.busurnode.com:443,https://rpc.sentineldao.com:443"
	c.RPCQueryTimeout = 10
	c.RPCTxTimeout = 30
	c.SimulateAndExecute = true

	return c
}

// EgressConfig is what the operator may relax in the egress policy every
// service applies (services/common/egress.go); the blocked networks are not
// configurable.
type EgressConfig struct {
	AllowSMTP bool `json:"allow_smtp" mapstructure:"allow_smtp"`
}

func NewEgressConfig() *EgressConfig {
	return &EgressConfig{}
}

func (c *EgressConfig) Validate() error {
	return nil
}

func (c *EgressConfig) WithDefaultValues() *EgressConfig {
	c.AllowSMTP = false

	return c
}

type GeoIPConfig struct {
	Provider  string  `json:"provider" mapstructure:"provider"`
	URL       string  `json:"url" mapstructure:"url"`
	Token     string  `json:"token" mapstructure:"token"`
	City      string  `json:"city" mapstructure:"city"`
	Country   string  `json:"country" mapstructure:"country"`
	Latitude  float64 `json:"latitude" mapstructure:"latitude"`
	Longitude float64 `json:"longitude" mapstructure:"longitude"`
}

func NewGeoIPConfig() *GeoIPConfig {
	return &GeoIPConfig{}
}

func (c *GeoIPConfig) Validate() error {
	switch c.Provider {
	case geoip.ProviderAuto, geoip.ProviderIPWhois, geoip.ProviderIP2Location, geoip.ProviderCloudflare,
		geoip.ProviderIPify, geoip.ProviderIPAPI, geoip.ProviderIPInfo, geoip.ProviderNone:
	default:
		return errors.New("provider must be one of auto, ipwhois, ip2location, cloudflare, ipify, ip-api, ipinfo, none")
	}
	if c.URL != "" {
		if c.Provider == geoip.ProviderAuto || c.Provider == geoip.ProviderNone {
			return fmt.Errorf("url cannot be set with provider %s", c.Provider)
		}
		u, err := url.ParseRequestURI(c.URL)
		if err != nil {
			return errors.Wrap(err, "invalid url")
		}
		// Over http the token would cross the network readable by anyone on
		// the path. Only ipinfo and ip2location are sent it.
		if c.Token != "" && u.Scheme != "https" &&
			(c.Provider == geoip.ProviderIPInfo || c.Provider == geoip.ProviderIP2Location) {
			return errors.New("url must be https when a token is set")
		}
	}
	// The token is a secret: it must only ever reach the host the operator named.
	switch c.Provider {
	case geoip.ProviderIPInfo:
		if c.Token == "" {
			return errors.New("token is required for provider ipinfo")
		}
	case geoip.ProviderIP2Location: // optional: the keyless tier works
	default:
		if c.Token != "" {
			return fmt.Errorf("token is only used by providers ipinfo and ip2location, not %s", c.Provider)
		}
	}
	if c.Latitude < -90 || c.Latitude > 90 {
		return errors.New("latitude must be between -90 and 90")
	}
	if c.Longitude < -180 || c.Longitude > 180 {
		return errors.New("longitude must be between -180 and 180")
	}

	return nil
}

func (c *GeoIPConfig) WithDefaultValues() *GeoIPConfig {
	c.Provider = geoip.ProviderAuto

	return c
}

type HandshakeConfig struct {
	Enable bool   `json:"enable" mapstructure:"enable"`
	Peers  uint64 `json:"peers" mapstructure:"peers"`
}

func NewHandshakeConfig() *HandshakeConfig {
	return &HandshakeConfig{}
}

func (c *HandshakeConfig) Validate() error {
	if c.Enable {
		if c.Peers <= 0 {
			return errors.New("peers must be positive")
		}
	}

	return nil
}

// WithDefaultValues leaves the Handshake resolver off: it needs hnsd, which is
// not installed with the node, and is of no use to a proxy node.
func (c *HandshakeConfig) WithDefaultValues() *HandshakeConfig {
	c.Enable = false
	c.Peers = 8

	return c
}

type KeyringConfig struct {
	Backend string `json:"backend" mapstructure:"backend"`
	From    string `json:"from" mapstructure:"from"`
	Granter string `json:"granter" mapstructure:"granter"`
}

func NewKeyringConfig() *KeyringConfig {
	return &KeyringConfig{}
}

func (c *KeyringConfig) Validate() error {
	if c.Backend == "" {
		return errors.New("backend cannot be empty")
	}
	if c.Backend != keyring.BackendFile && c.Backend != keyring.BackendTest {
		return fmt.Errorf("backend must be either %s or %s", keyring.BackendFile, keyring.BackendTest)
	}
	if c.From == "" {
		return errors.New("from cannot be empty")
	}
	if c.Granter != "" {
		if _, err := sdk.AccAddressFromBech32(c.Granter); err != nil {
			return errors.Wrap(err, "invalid granter")
		}
	}

	return nil
}

func (c *KeyringConfig) WithDefaultValues() *KeyringConfig {
	c.Backend = keyring.BackendFile
	c.From = "operator"

	return c
}

type NodeConfig struct {
	IntervalSetSessions    time.Duration `json:"interval_set_sessions" mapstructure:"interval_set_sessions"`
	IntervalUpdateSessions time.Duration `json:"interval_update_sessions" mapstructure:"interval_update_sessions"`
	IntervalUpdateStatus   time.Duration `json:"interval_update_status" mapstructure:"interval_update_status"`
	IPv4Address            string        `json:"ipv4_address" mapstructure:"ipv4_address"`
	ListenOn               string        `json:"listen_on" mapstructure:"listen_on"`
	Moniker                string        `json:"moniker" mapstructure:"moniker"`
	GigabytePrices         string        `json:"gigabyte_prices" mapstructure:"gigabyte_prices"`
	HourlyPrices           string        `json:"hourly_prices" mapstructure:"hourly_prices"`
	RemoteURL              string        `json:"remote_url" mapstructure:"remote_url"`
	Type                   string        `json:"type" mapstructure:"type"`
	LegacyHandshake        bool          `json:"legacy_handshake" mapstructure:"legacy_handshake"`
}

func NewNodeConfig() *NodeConfig {
	return &NodeConfig{}
}

func (c *NodeConfig) Validate() error {
	if c.IntervalSetSessions < MinIntervalSetSessions {
		return fmt.Errorf("interval_set_sessions cannot be less than %s", MinIntervalSetSessions)
	}
	if c.IntervalSetSessions > MaxIntervalSetSessions {
		return fmt.Errorf("interval_set_sessions cannot be greater than %s", MaxIntervalSetSessions)
	}
	if c.IntervalUpdateSessions < MinIntervalUpdateSessions {
		return fmt.Errorf("interval_update_sessions cannot be less than %s", MinIntervalUpdateSessions)
	}
	if c.IntervalUpdateSessions > MaxIntervalUpdateSessions {
		return fmt.Errorf("interval_update_sessions cannot be greater than %s", MaxIntervalUpdateSessions)
	}
	if c.IntervalUpdateStatus < MinIntervalUpdateStatus {
		return fmt.Errorf("interval_update_status cannot be less than %s", MinIntervalUpdateStatus)
	}
	if c.IntervalUpdateStatus > MaxIntervalUpdateStatus {
		return fmt.Errorf("interval_update_status cannot be greater than %s", MaxIntervalUpdateStatus)
	}
	if c.IPv4Address != "" {
		addr := net.ParseIP(c.IPv4Address)
		if addr == nil {
			return errors.New("invalid ipv4_address")
		}
		if addr.To4() == nil {
			return errors.New("ipv4_address format must be in IPv4 format")
		}
	}
	if c.ListenOn == "" {
		return errors.New("listen_on cannot be empty")
	}
	// The API's port also goes into the egress policy, which reads a
	// missing one as 0.
	if _, port, err := net.SplitHostPort(c.ListenOn); err != nil {
		return errors.Wrap(err, "invalid listen_on")
	} else if v, err := strconv.ParseUint(port, 10, 16); err != nil || v == 0 {
		return fmt.Errorf("listen_on port must be a number from 1 to 65535, not %q", port)
	}
	if c.Moniker == "" {
		return errors.New("moniker cannot be empty")
	}
	if len(c.Moniker) < MinMonikerLength {
		return fmt.Errorf("moniker length cannot be less than %d", MinMonikerLength)
	}
	if len(c.Moniker) > MaxMonikerLength {
		return fmt.Errorf("moniker length cannot be greater than %d", MaxMonikerLength)
	}
	if c.GigabytePrices == "" {
		return fmt.Errorf("gigabyte_prices cannot be empty")
	}
	if _, err := ParsePrices(c.GigabytePrices); err != nil {
		return errors.Wrap(err, "invalid gigabyte_prices")
	}
	if c.HourlyPrices == "" {
		return fmt.Errorf("hourly_prices cannot be empty")
	}
	if _, err := ParsePrices(c.HourlyPrices); err != nil {
		return errors.Wrap(err, "invalid hourly_prices")
	}
	if c.RemoteURL == "" {
		return errors.New("remote_url cannot be empty")
	}

	remoteURL, err := url.ParseRequestURI(c.RemoteURL)
	if err != nil {
		return errors.Wrap(err, "invalid remote_url")
	}
	if remoteURL.Scheme != "https" {
		return errors.New("remote_url scheme must be https")
	}
	if remoteURL.Port() == "" {
		return errors.New("remote_url port cannot be empty")
	}

	if c.Type == "" {
		return errors.New("type cannot be empty")
	}
	if !nodeTypeKnown(c.Type) {
		return errors.Errorf("type must be one of: %s", nodeTypeList())
	}

	return nil
}

func (c *NodeConfig) WithDefaultValues() *NodeConfig {
	c.IntervalSetSessions = 10 * time.Second
	c.IntervalUpdateSessions = MaxIntervalUpdateSessions
	c.IntervalUpdateStatus = MaxIntervalUpdateStatus
	c.ListenOn = fmt.Sprintf("0.0.0.0:%d", utils.RandomPort())
	c.Type = "wireguard"
	c.LegacyHandshake = false

	return c
}

type QOSConfig struct {
	MaxPeers int `json:"max_peers" mapstructure:"max_peers"`
}

func NewQOSConfig() *QOSConfig {
	return &QOSConfig{}
}

func (c *QOSConfig) Validate() error {
	if c.MaxPeers < MinPeers {
		return fmt.Errorf("max_peers cannot be less than %d", MinPeers)
	}
	if c.MaxPeers > MaxPeers {
		return fmt.Errorf("max_peers cannot be greater than %d", MaxPeers)
	}

	return nil
}

func (c *QOSConfig) WithDefaultValues() *QOSConfig {
	c.MaxPeers = MaxPeers

	return c
}

type Config struct {
	Bandwidth *BandwidthConfig `json:"bandwidth" mapstructure:"bandwidth"`
	Chain     *ChainConfig     `json:"chain" mapstructure:"chain"`
	Egress    *EgressConfig    `json:"egress" mapstructure:"egress"`
	GeoIP     *GeoIPConfig     `json:"geoip" mapstructure:"geoip"`
	Handshake *HandshakeConfig `json:"handshake" mapstructure:"handshake"`
	Keyring   *KeyringConfig   `json:"keyring" mapstructure:"keyring"`
	Node      *NodeConfig      `json:"node" mapstructure:"node"`
	QOS       *QOSConfig       `json:"qos" mapstructure:"qos"`
}

func NewConfig() *Config {
	return &Config{
		Bandwidth: NewBandwidthConfig(),
		Chain:     NewChainConfig(),
		Egress:    NewEgressConfig(),
		GeoIP:     NewGeoIPConfig(),
		Handshake: NewHandshakeConfig(),
		Keyring:   NewKeyringConfig(),
		Node:      NewNodeConfig(),
		QOS:       NewQOSConfig(),
	}
}

func (c *Config) Validate() error {
	if err := c.Bandwidth.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section bandwidth")
	}
	if err := c.Chain.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section chain")
	}
	if err := c.Egress.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section egress")
	}
	if err := c.GeoIP.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section geoip")
	}
	if err := c.Handshake.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section handshake")
	}
	if err := c.Keyring.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section keyring")
	}
	if err := c.Node.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section node")
	}
	if err := c.QOS.Validate(); err != nil {
		return errors.Wrapf(err, "invalid section qos")
	}

	if c.GeoIP.Provider == geoip.ProviderNone && c.Node.IPv4Address == "" {
		return errors.Wrapf(errors.New("ipv4_address must be set when geoip.provider is none"), "invalid section node")
	}

	if c.Handshake.Enable && !nodeTypeAllowsHandshakeDNS(c.Node.Type) {
		return errors.Wrapf(errors.Errorf("must be disabled for a %s node", c.Node.Type), "invalid section handshake")
	}

	return nil
}

func (c *Config) WithDefaultValues() *Config {
	c.Bandwidth = c.Bandwidth.WithDefaultValues()
	c.Chain = c.Chain.WithDefaultValues()
	c.Egress = c.Egress.WithDefaultValues()
	c.GeoIP = c.GeoIP.WithDefaultValues()
	c.Handshake = c.Handshake.WithDefaultValues()
	c.Keyring = c.Keyring.WithDefaultValues()
	c.Node = c.Node.WithDefaultValues()
	c.QOS = c.QOS.WithDefaultValues()

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
	var buf bytes.Buffer
	if err := t.Execute(&buf, c); err != nil {
		panic(err)
	}

	return buf.String()
}

// Redacted is the configuration as the node logs it: a copy with the GeoIP
// token, its only secret, blanked. Logs travel further than the config file
// (the journal, docker logs, a pasted support request).
func (c *Config) Redacted() *Config {
	v := *c
	if c.GeoIP != nil && c.GeoIP.Token != "" {
		geoip := *c.GeoIP
		geoip.Token = "<redacted>"
		v.GeoIP = &geoip
	}

	return &v
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

// ParsePrices accepts either the chain's own price notation
// ("udvpn:0.5,1000;uatom:0,20") or plain coins ("1000udvpn,20uatom"), the format
// upstream used before prices gained a base value. An empty string yields no prices.
func ParsePrices(s string) (v1base.Prices, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return v1base.Prices{}, nil
	}
	if strings.Contains(s, ":") {
		return v1base.NewPricesFromString(s)
	}

	coins, err := sdk.ParseCoinsNormalized(s)
	if err != nil {
		return nil, err
	}

	return v1base.NewPricesFromCoins(coins...)
}
