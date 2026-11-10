// SPDX-License-Identifier: Apache-2.0

// Package openvpn runs an OpenVPN server as the node's VPN service. The node
// is its own certificate authority: it issues a certificate and key per
// session, which the client presents; the server asks the node over its
// management interface whether the certificate's common name is a registered
// peer. Usage comes from the server's status table, and a peer is removed by
// killing its connection and forgetting it. See docs/protocols.md.
package openvpn

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	ovpntypes "github.com/Feryx-Official/dvpnd/v9/services/openvpn/types"
	"github.com/Feryx-Official/dvpnd/v9/services/wireguard"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

const (
	// InfoLen: port (2), transport (1: 1 udp, 2 tcp), IPv6 flag (1).
	InfoLen = 2 + 1 + 1

	transportUDP = 1
	transportTCP = 2

	// The tunnel networks the server hands addresses from; distinct from the
	// WireGuard pools so both could coexist on one host.
	ipv4Network = "10.9.0.0"
	ipv4Netmask = "255.255.255.0"
	ipv6Network = "fd86:ea04:1116::/112"
)

var (
	_ types.Service = (*OpenVPN)(nil)

	// binaryName is the server binary looked up on PATH; a variable so tests
	// can point it at a stub.
	binaryName = "openvpn"

	// stopTimeout is how long Stop waits after SIGTERM before killing.
	stopTimeout = 10 * time.Second

	// managementTimeout is how long Start waits for the management socket.
	managementTimeout = 15 * time.Second

	// ensureForwarding turns IP forwarding on; a variable so tests can skip it.
	ensureForwarding = wireguard.EnsureForwarding
)

// traffic is a peer's accumulated bytes from connections that have ended;
// the live connection's counters come from the status table.
type traffic struct {
	upload   int64
	download int64
}

type OpenVPN struct {
	info       []byte
	config     *ovpntypes.Config
	pki        *pki
	process    *common.Process
	mgmt       *management
	nat        common.NAT
	egress     common.TunnelEgress
	configPath string
	socketPath string

	mu     sync.Mutex
	byCN   map[string]string   // certificate common name → session key
	closed map[string]*traffic // session key → traffic of ended connections
	peers  *common.PeerSet
}

func NewOpenVPN() *OpenVPN {
	return &OpenVPN{
		info:   make([]byte, InfoLen),
		config: ovpntypes.NewConfig(),
		byCN:   map[string]string{},
		closed: map[string]*traffic{},
		peers:  common.NewPeerSet(),
	}
}

func (s *OpenVPN) Type() uint64 {
	return ovpntypes.Type
}

func (s *OpenVPN) Info() []byte {
	return s.info
}

func (s *OpenVPN) Init(home string) (err error) {
	if _, err = exec.LookPath(binaryName); err != nil {
		return fmt.Errorf("the %q binary is not on PATH: install the openvpn package on the host "+
			"(docs/operator.md, section 6) or run the Docker image, which bundles it: %w",
			binaryName, err)
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, ovpntypes.ConfigFileName))

	s.config, err = ovpntypes.ReadInConfig(v)
	if err != nil {
		return err
	}
	if err = s.config.Validate(); err != nil {
		return err
	}
	if s.config.Uplink == "" {
		s.config.Uplink = wireguard.DetectUplink()
	}

	s.pki, err = loadOrCreatePKI(filepath.Join(home, "openvpn"))
	if err != nil {
		return err
	}

	data := templateData{
		Interface:   s.config.Interface,
		Proto:       s.config.Proto,
		ListenPort:  s.config.ListenPort,
		EnableIPv6:  s.config.EnableIPv6,
		IPv4Network: ipv4Network,
		IPv4Netmask: ipv4Netmask,
		IPv6Network: ipv6Network,
		CACert:      s.pki.caCertPath(),
		ServerCert:  s.pki.serverCertPath(),
		ServerKey:   s.pki.serverKeyPath(),
		TLSCrypt:    s.pki.tlsCryptPath(),
		Verb:        verb(),
	}

	// The server reads its keys and opens the tunnel as root, then drops to
	// the proxy account when the node has one; the management socket goes to
	// the runtime directory, where only root may connect to it.
	rt := common.CurrentRuntime()
	s.socketPath = filepath.Join(rt.Dir, "openvpn.sock")
	data.ManagementSocket = s.socketPath
	// A socket left by a server that died must not answer for this one.
	_ = os.Remove(s.socketPath)
	if rt.Proxy != nil {
		data.User, data.Group = rt.Proxy.Name, rt.Proxy.Group
	}

	t, err := template.New("openvpn_conf").Parse(configTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err = t.Execute(&buf, data); err != nil {
		return err
	}
	if s.configPath, err = rt.WriteFile("openvpn_server.conf", buf.Bytes()); err != nil {
		return err
	}

	s.nat = common.NAT{Interface: s.config.Interface, Uplink: s.config.Uplink}
	s.egress = common.TunnelEgress{Interface: s.config.Interface}

	binary.BigEndian.PutUint16(s.info[0:], s.config.ListenPort)
	s.info[2] = transportUDP
	if s.config.Proto == ovpntypes.ProtoTCP {
		s.info[2] = transportTCP
	}
	if s.config.EnableIPv6 {
		s.info[3] = 1
	}

	return nil
}

// TunnelIPv4 is the node's address inside the tunnel: OpenVPN's server
// directive gives the server the first host of the network.
func (s *OpenVPN) TunnelIPv4() net.IP {
	ip := net.ParseIP(ipv4Network).To4()

	return net.IPv4(ip[0], ip[1], ip[2], ip[3]+1)
}

// Start installs the egress firewall, launches the server, attaches to its
// management interface (nothing is admitted until the node is attached) and
// installs the NAT rules.
func (s *OpenVPN) Start() (err error) {
	if err = ensureForwarding(); err != nil {
		return err
	}

	if err = s.egress.Up(); err != nil {
		s.egress.Down()

		return err
	}

	s.process, err = common.StartProcess(binaryName, []string{"--config", s.configPath}, nil, nil)
	if err != nil {
		s.egress.Down()

		return err
	}

	conn, err := dialManagement(s.socketPath, managementTimeout)
	if err != nil {
		_ = s.process.Stop(stopTimeout)
		s.egress.Down()

		return err
	}
	s.mgmt = newManagement(conn, s.admit, s.disconnected)

	if err = s.nat.Up(); err != nil {
		_ = s.mgmt.Close()
		_ = s.process.Stop(stopTimeout)
		s.nat.Down()
		s.egress.Down()

		return err
	}

	return nil
}

// Stop detaches from the management interface, ends the server and removes
// the NAT rules and the egress firewall.
func (s *OpenVPN) Stop() error {
	if s.process == nil {
		return errors.New("openvpn was not started")
	}

	if s.mgmt != nil {
		_ = s.mgmt.Close()
		s.mgmt = nil
	}
	err := s.process.Stop(stopTimeout)
	s.nat.Down()
	s.egress.Down()

	return err
}

// admit is the management-client-auth decision: only a registered peer's
// certificate may connect.
func (s *OpenVPN) admit(commonName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byCN[commonName]

	return ok
}

// disconnected banks the traffic of a connection that ended, so a
// reconnecting client's counters start again without losing what it used.
func (s *OpenVPN) disconnected(commonName string, received, sent int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, ok := s.byCN[commonName]
	if !ok {
		return
	}
	c := s.closed[key]
	if c == nil {
		c = &traffic{}
		s.closed[key] = c
	}
	c.upload += received
	c.download += sent
}

func peerKey(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func checkPeerData(data []byte) error {
	if len(data) != common.UUIDLen {
		return fmt.Errorf("peer data must be a %d-byte uuid", common.UUIDLen)
	}

	return nil
}

// AddPeer issues the peer's certificate and key and returns them for the
// handshake: a 4-byte big-endian certificate length, the certificate DER,
// then the PKCS#8 key DER.
func (s *OpenVPN) AddPeer(data []byte) ([]byte, error) {
	if err := checkPeerData(data); err != nil {
		return nil, err
	}
	if s.pki == nil {
		return nil, errors.New("openvpn was not initialised")
	}

	var id [common.UUIDLen]byte
	copy(id[:], data)
	cn := common.FormatUUID(id)
	key := peerKey(data)

	certDER, keyDER, err := s.pki.issueClient(cn)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.byCN[cn] = key
	if _, ok := s.closed[key]; !ok {
		s.closed[key] = &traffic{}
	}
	s.mu.Unlock()
	s.peers.Put(key)

	result := make([]byte, 4, 4+len(certDER)+len(keyDER))
	binary.BigEndian.PutUint32(result, uint32(len(certDER)))
	result = append(result, certDER...)
	result = append(result, keyDER...)

	return result, nil
}

func (s *OpenVPN) HasPeer(data []byte) bool {
	return s.peers.Has(peerKey(data))
}

// RemovePeer forgets the peer, so its certificate is denied from now on, and
// kills its connection if it has one.
func (s *OpenVPN) RemovePeer(data []byte) error {
	if err := checkPeerData(data); err != nil {
		return err
	}

	var id [common.UUIDLen]byte
	copy(id[:], data)
	cn := common.FormatUUID(id)
	key := peerKey(data)

	s.mu.Lock()
	delete(s.byCN, cn)
	delete(s.closed, key)
	s.mu.Unlock()
	s.peers.Delete(key)

	if s.mgmt == nil {
		return nil
	}

	return s.mgmt.kill(cn)
}

// Peers reports every registered peer: what its ended connections used plus
// the live connection's counters from the status table.
func (s *OpenVPN) Peers() ([]types.Peer, error) {
	live := map[string]clientStatus{}
	if s.mgmt != nil {
		rows, err := s.mgmt.status()
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			live[row.commonName] = row
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	keys := s.peers.Keys()
	items := make([]types.Peer, 0, len(keys))
	for _, key := range keys {
		item := types.Peer{Key: key}
		if c := s.closed[key]; c != nil {
			item.Upload, item.Download = c.upload, c.download
		}
		for cn, k := range s.byCN {
			if k != key {
				continue
			}
			if row, ok := live[cn]; ok {
				item.Upload += row.received
				item.Download += row.sent
			}
		}
		items = append(items, item)
	}

	return items, nil
}

func (s *OpenVPN) PeerCount() int {
	return s.peers.Len()
}
