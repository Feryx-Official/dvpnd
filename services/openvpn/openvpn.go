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
	nat        common.NAT
	egress     common.TunnelEgress
	configPath string
	socketPath string
	done       <-chan struct{} // closed once the server or the management channel has ended

	mu     sync.Mutex
	mgmt   *management         // nil before Start and after Stop
	byCN   map[string]string   // certificate common name → session key
	closed map[string]*traffic // session key → traffic of ended connections
	// revoked holds the common names of peers being removed: denied at
	// once, still counted until their connection is killed.
	revoked map[string]struct{}
	// banked holds the client ids of ended connections whose traffic is in
	// closed, until a status table no longer lists them.
	banked map[string]struct{}
	// seen is each live connection's row in the last status table read.
	seen  map[string]clientStatus
	peers *common.PeerSet
}

func NewOpenVPN() *OpenVPN {
	return &OpenVPN{
		info:    make([]byte, InfoLen),
		config:  ovpntypes.NewConfig(),
		byCN:    map[string]string{},
		closed:  map[string]*traffic{},
		revoked: map[string]struct{}{},
		banked:  map[string]struct{}{},
		seen:    map[string]clientStatus{},
		peers:   common.NewPeerSet(),
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
	mgmt := newManagement(conn, s.admit, s.disconnected)
	s.mu.Lock()
	s.mgmt = mgmt
	s.mu.Unlock()

	if err = s.nat.Up(); err != nil {
		_ = s.detach()
		_ = s.process.Stop(stopTimeout)
		s.nat.Down()
		s.egress.Down()

		return err
	}
	s.done = common.EitherDone(s.process.Done(), mgmt.done)

	return nil
}

// Done is closed once the server has exited or the management channel has
// closed: without it the node admits no one and counts no traffic.
func (s *OpenVPN) Done() <-chan struct{} {
	return s.done
}

// Stop detaches from the management interface, ends the server and removes
// the NAT rules and the egress firewall.
func (s *OpenVPN) Stop() error {
	if s.process == nil {
		return errors.New("openvpn was not started")
	}

	_ = s.detach()
	err := s.process.Stop(stopTimeout)
	s.nat.Down()
	s.egress.Down()

	return err
}

// attached returns the connection to the management interface, nil before
// Start and after Stop. The jobs use it while Stop may drop it.
func (s *OpenVPN) attached() *management {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.mgmt
}

// detach drops the management connection and closes it. It is closed
// outside the lock: the reader it waits for may be delivering a disconnect,
// which takes the lock.
func (s *OpenVPN) detach() error {
	s.mu.Lock()
	m := s.mgmt
	s.mgmt = nil
	s.mu.Unlock()

	if m == nil {
		return nil
	}

	return m.Close()
}

// admit is the management-client-auth decision: only a registered peer's
// certificate may connect, and not once its removal has begun.
func (s *OpenVPN) admit(commonName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byCN[commonName]
	_, revoked := s.revoked[commonName]

	return ok && !revoked
}

// disconnected banks the traffic of a connection that ended, so a
// reconnecting client's counters start again without losing what it used. A
// count the server sent unreadable (-1) is taken from the last status table
// read: a zero would make the peer's total drop.
func (s *OpenVPN) disconnected(commonName, cid string, received, sent int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, ok := s.byCN[commonName]
	if !ok {
		return
	}
	if received < 0 {
		received = s.seen[cid].received
	}
	if sent < 0 {
		sent = s.seen[cid].sent
	}
	c := s.closed[key]
	if c == nil {
		c = &traffic{}
		s.closed[key] = c
	}
	c.upload += received
	c.download += sent
	s.banked[cid] = struct{}{}
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
	// A peer still registered here is one whose kill failed: its open
	// connection is counted until the next pass removes it.
	if s.peers.Has(key) {
		s.mu.Unlock()

		return nil, errors.New("the peer is still registered: its removal has not finished")
	}
	s.byCN[cn] = key
	s.closed[key] = &traffic{}
	s.peers.Put(key)
	s.mu.Unlock()

	result := make([]byte, 4, 4+len(certDER)+len(keyDER))
	binary.BigEndian.PutUint32(result, uint32(len(certDER)))
	result = append(result, certDER...)
	result = append(result, keyDER...)

	return result, nil
}

func (s *OpenVPN) HasPeer(data []byte) bool {
	return s.peers.Has(peerKey(data))
}

// RemovePeer denies the peer's certificate from now on and kills its
// connection if it has one. The peer stays registered, and its traffic
// counted, until the kill succeeds: a connection the kill missed keeps
// working, and the next pass removes the peer again.
func (s *OpenVPN) RemovePeer(data []byte) error {
	if err := checkPeerData(data); err != nil {
		return err
	}

	var id [common.UUIDLen]byte
	copy(id[:], data)
	cn := common.FormatUUID(id)
	key := peerKey(data)

	s.mu.Lock()
	s.revoked[cn] = struct{}{}
	m := s.mgmt
	s.mu.Unlock()

	if m != nil {
		if err := m.kill(cn); err != nil {
			return err
		}
	}

	s.mu.Lock()
	delete(s.byCN, cn)
	delete(s.closed, key)
	delete(s.revoked, cn)
	s.peers.Delete(key)
	s.mu.Unlock()

	return nil
}

// Peers reports every registered peer: what its ended connections used plus
// the live connection's counters from the status table. The table is read
// before the lock is taken, so a connection that ended in between is both
// banked and listed; it counts once, as banked.
func (s *OpenVPN) Peers() ([]types.Peer, error) {
	live := map[string]clientStatus{}
	if m := s.attached(); m != nil {
		rows, err := m.status()
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			live[row.commonName] = row
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// One pass over the peers and one over the live rows, which the
	// admission callback waits for.
	keys := s.peers.Keys()
	items := make([]types.Peer, len(keys))
	byKey := make(map[string]*types.Peer, len(keys))
	for i, key := range keys {
		items[i].Key = key
		if c := s.closed[key]; c != nil {
			items[i].Upload, items[i].Download = c.upload, c.download
		}
		byKey[key] = &items[i]
	}
	for cn, row := range live {
		item := byKey[s.byCN[cn]]
		if _, ended := s.banked[row.cid]; item != nil && !ended {
			item.Upload += row.received
			item.Download += row.sent
		}
	}

	// A table read from now on no longer lists a connection that ended.
	listed := map[string]bool{}
	for _, row := range live {
		listed[row.cid] = true
	}
	for cid := range s.banked {
		if !listed[cid] {
			delete(s.banked, cid)
		}
	}
	s.seen = make(map[string]clientStatus, len(live))
	for _, row := range live {
		s.seen[row.cid] = row
	}

	return items, nil
}

func (s *OpenVPN) PeerCount() int {
	return s.peers.Len()
}
