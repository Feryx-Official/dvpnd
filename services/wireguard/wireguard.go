// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package wireguard

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

const (
	InfoLen = 2 + 32
)

var (
	_ types.Service = (*WireGuard)(nil)

	// ensureForwarding turns IP forwarding on; a variable so tests can skip it.
	ensureForwarding = common.EnsureForwarding

	// quickTimeout bounds wg-quick, which runs ip, wg and iptables in turn.
	quickTimeout = 2 * time.Minute

	// kernelHasIPv6 says whether the kernel has IPv6; a variable so tests
	// can take it away.
	kernelHasIPv6 = common.KernelHasIPv6
)

// Variant is one implementation of the WireGuard family: the tools that
// drive it and where they look for the interface configuration. AmneziaWG
// is WireGuard with obfuscation and its own tools, so it reuses this
// service through a Variant.
type Variant struct {
	Name      string // service_type clients expect
	Type      uint64 // numeric service type
	Tool      string // "wg": configures the interface
	Quick     string // "wg-quick": brings it up and down
	ConfigDir string // where Quick reads <interface>.conf
}

// Default is plain WireGuard.
var Default = Variant{
	Name:      Name,
	Type:      wgtypes.Type,
	Tool:      "wg",
	Quick:     "wg-quick",
	ConfigDir: "/etc/wireguard",
}

type WireGuard struct {
	variant Variant
	info    []byte
	config  *wgtypes.Config
	peers   *wgtypes.Peers
	pool    *wgtypes.IPPool

	// preset, when set, replaces reading wireguard.toml in Init: a variant
	// with its own configuration file hands the WireGuard part over here.
	preset *wgtypes.Config
	// extra are additional [Interface] lines a variant needs.
	extra []string
}

func NewWireGuard(pool *wgtypes.IPPool) types.Service {
	return NewVariant(Default, pool)
}

// NewVariant builds the service for another member of the family.
func NewVariant(variant Variant, pool *wgtypes.IPPool) *WireGuard {
	return &WireGuard{
		variant: variant,
		pool:    pool,
		config:  wgtypes.NewConfig(),
		info:    make([]byte, InfoLen),
		peers:   wgtypes.NewPeers(),
	}
}

// WithPool replaces the tunnel address pool; a variant whose networks come
// from its configuration sets it before Init.
func (s *WireGuard) WithPool(pool *wgtypes.IPPool) *WireGuard {
	s.pool = pool

	return s
}

// WithConfig makes Init use this configuration, and these extra [Interface]
// lines, instead of reading wireguard.toml.
func (s *WireGuard) WithConfig(config *wgtypes.Config, extra []string) *WireGuard {
	s.preset = config
	s.extra = extra

	return s
}

func (s *WireGuard) Type() uint64 {
	return s.variant.Type
}

// Config is the configuration in force after Init.
func (s *WireGuard) Config() *wgtypes.Config {
	return s.config
}

// interfaceConfig is what the wg-quick configuration template renders.
type interfaceConfig struct {
	*wgtypes.Config
	// Address is the interface's own tunnel addresses, from its pools.
	Address string
	// PostUp and PostDown add and remove the forwarding and NAT rules.
	PostUp, PostDown string
	Extra            []string
}

// renderInterface is the wg-quick configuration of an interface whose peers
// get addresses from pool. Without IPv6 the interface gets no IPv6 address
// and PostUp calls no ip6tables.
func renderInterface(cfg *wgtypes.Config, pool *wgtypes.IPPool, extra []string) ([]byte, error) {
	t, err := template.New("wireguard_conf").Parse(configTemplate)
	if err != nil {
		return nil, err
	}

	networks := []*net.IPNet{pool.V4.Net}
	if cfg.EnableIPv6 {
		networks = append(networks, pool.V6.Net)
	}
	up, down := common.NAT{Interface: "%i", Uplink: cfg.Uplink, Networks: networks}.Hooks()

	var buffer bytes.Buffer
	err = t.Execute(&buffer, interfaceConfig{
		Config:   cfg,
		Address:  TunnelAddress(pool, cfg.EnableIPv6),
		PostUp:   up,
		PostDown: down,
		Extra:    extra,
	})

	return buffer.Bytes(), err
}

// TunnelAddress is the interface's own address in each of the pools it
// hands peers addresses from, the IPv6 one only when the tunnel carries
// IPv6: the first host of the pool's network, with the network's prefix
// length.
func TunnelAddress(pool *wgtypes.IPPool, ipv6 bool) string {
	v4bits, _ := pool.V4.Net.Mask.Size()
	v4 := wgtypes.NewIPv4FromIP(pool.V4.Net.IP).Next().IP()
	address := fmt.Sprintf("%s/%d", v4, v4bits)
	if ipv6 {
		v6bits, _ := pool.V6.Net.Mask.Size()
		v6 := wgtypes.NewIPv6FromIP(pool.V6.Net.IP).Next().IP()
		address += fmt.Sprintf(",%s/%d", v6, v6bits)
	}

	return address
}

// TunnelIPv4 is the node's IPv4 address on the tunnel interface, the first
// host of the pool (the address TunnelAddress gives the interface).
func (s *WireGuard) TunnelIPv4() net.IP {
	return wgtypes.NewIPv4FromIP(s.pool.V4.Net.IP).Next().IP()
}

func (s *WireGuard) Init(home string) (err error) {
	if s.preset != nil {
		s.config = s.preset
	} else {
		v := viper.New()
		v.SetConfigFile(filepath.Join(home, wgtypes.ConfigFileName))

		s.config, err = wgtypes.ReadInConfig(v)
		if err != nil {
			return err
		}
	}
	if err = s.config.Validate(); err != nil {
		return err
	}

	if s.config.Uplink == "" {
		if s.config.Uplink, err = common.DetectUplink(); err != nil {
			return err
		}
	}
	if s.config.EnableIPv6 && !kernelHasIPv6() {
		fmt.Fprintf(os.Stderr, "warning: enable_ipv6 is on but the kernel has no IPv6; %s carries IPv4 only\n", s.config.Interface)
		s.config.EnableIPv6 = false
	}

	conf, err := renderInterface(s.config, s.pool, s.extra)
	if err != nil {
		return err
	}

	if err = os.MkdirAll(s.variant.ConfigDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.variant.ConfigDir, s.config.Interface+".conf")
	if err = os.WriteFile(path, conf, 0600); err != nil {
		return err
	}

	key, err := wgtypes.KeyFromString(s.config.PrivateKey)
	if err != nil {
		return err
	}

	binary.BigEndian.PutUint16(s.info[:2], s.config.ListenPort)
	copy(s.info[2:], key.Public().Bytes())

	return nil
}

func (s *WireGuard) Info() []byte {
	return s.info
}

func (s *WireGuard) wgQuick(action string) error {
	return common.RunTool(quickTimeout, s.variant.Quick, action, s.config.Interface)
}

// egress is the firewall that keeps the interface's peers off the host and
// the networks around it.
func (s *WireGuard) egress() common.TunnelEgress {
	return common.TunnelEgress{Interface: s.config.Interface}
}

// Start turns IP forwarding on, then brings the interface up behind its
// egress firewall.
func (s *WireGuard) Start() error {
	if err := ensureForwarding(); err != nil {
		return err
	}

	return s.Up()
}

// Up installs the egress firewall, then brings the interface up, so no peer
// packet is forwarded unfiltered. An earlier instance that died without Stop
// leaves the interface behind and wg-quick refuses to create it again; in
// that case it is torn down and recreated so a restart needs no manual
// cleanup. IP forwarding is the caller's: Start turns it on, and a variant
// with two interfaces turns it on once.
func (s *WireGuard) Up() (err error) {
	if err = s.egress().Up(); err != nil {
		s.egress().Down()
		return err
	}
	defer func() {
		if err != nil {
			s.egress().Down()
		}
	}()

	err = s.wgQuick("up")
	if err == nil {
		return nil
	}

	if _, statErr := os.Stat(filepath.Join("/sys/class/net", s.config.Interface)); statErr != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "interface %s already exists; recreating it\n", s.config.Interface)
	_ = s.wgQuick("down")

	return s.wgQuick("up")
}

// Stop takes the interface down, then removes the egress firewall.
func (s *WireGuard) Stop() error {
	err := s.wgQuick("down")
	s.egress().Down()

	return err
}

func (s *WireGuard) AddPeer(data []byte) (result []byte, err error) {
	identity := base64.StdEncoding.EncodeToString(data)

	v4, v6, err := s.pool.Get()
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			s.pool.Release(v4, v6)
		}
	}()

	// With IPv6 off the peer gets only an IPv4 tunnel address: the v6 slot in the
	// result stays zero, which tells the handshake to hand the client just the
	// IPv4 address, so it routes nothing over IPv6 through the tunnel.
	allowedIPs := fmt.Sprintf("%s/32", v4.IP())
	if s.config.EnableIPv6 {
		allowedIPs += fmt.Sprintf(",%s/128", v6.IP())
	} else {
		s.pool.V6.Release(v6)
		v6 = wgtypes.IPv6{}
	}

	err = common.RunTool(common.ToolTimeout, s.variant.Tool, strings.Split(
		fmt.Sprintf(`set %s peer %s allowed-ips %s`,
			s.config.Interface, identity, allowedIPs), " ")...)
	if err != nil {
		return nil, err
	}

	s.peers.Put(
		wgtypes.Peer{
			Identity: identity,
			IPv4:     v4,
			IPv6:     v6,
		},
	)

	result = append(result, v4.Bytes()...)
	result = append(result, v6.Bytes()...)
	return result, nil
}

func (s *WireGuard) HasPeer(data []byte) bool {
	var (
		identity = base64.StdEncoding.EncodeToString(data)
		peer     = s.peers.Get(identity)
	)

	return !peer.Empty()
}

func (s *WireGuard) RemovePeer(data []byte) error {
	identity := base64.StdEncoding.EncodeToString(data)

	err := common.RunTool(common.ToolTimeout, s.variant.Tool, strings.Split(
		fmt.Sprintf(`set %s peer %s remove`,
			s.config.Interface, identity), " ")...)
	if err != nil {
		return err
	}

	if v := s.peers.Get(identity); !v.Empty() {
		s.peers.Delete(v.Identity)
		s.pool.Release(v.IPv4, v.IPv6)
	}

	return nil
}

func (s *WireGuard) Peers() (items []types.Peer, err error) {
	output, err := common.ToolOutput(common.ToolTimeout, s.variant.Tool, strings.Split(
		fmt.Sprintf("show %s transfer", s.config.Interface), " ")...)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		columns := strings.Split(line, "\t")
		if len(columns) != 3 {
			continue
		}

		upload, err := strconv.ParseInt(columns[1], 10, 64)
		if err != nil {
			return nil, err
		}

		download, err := strconv.ParseInt(columns[2], 10, 64)
		if err != nil {
			return nil, err
		}

		items = append(items,
			types.Peer{
				Key:      columns[0],
				Upload:   upload,
				Download: download,
			},
		)
	}

	return items, nil
}

func (s *WireGuard) PeerCount() int {
	return s.peers.Len()
}
