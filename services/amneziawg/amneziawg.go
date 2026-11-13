// SPDX-License-Identifier: Apache-2.0

// Package amneziawg runs an AmneziaWG tunnel as the node's VPN service.
// AmneziaWG is WireGuard with obfuscation: junk packets before the
// handshake, padding on the handshake messages and replaced message type
// values. The data plane, peers and usage are the WireGuard service's,
// driven with the awg tools; this package adds the parameters, which
// clients receive in the handshake, on up to two interfaces: the default
// tier, whose parameter set every client engine from AmneziaWG 1.0 up
// accepts and which current apps get without asking, and the AmneziaWG 3.1
// tier (header protection, random trailers), handed only to a client that
// asks for awg_version 3 in its peer request. Header protection is an
// interface-wide setting, so the tiers cannot share an interface. See
// docs/protocols.md.
package amneziawg

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	awgtypes "github.com/Feryx-Official/dvpnd/v9/services/amneziawg/types"
	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/services/wireguard"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// Name is the service_type string clients expect.
const Name = "amneziawg"

// Variant drives the tunnel with the AmneziaWG tools.
var Variant = wireguard.Variant{
	Name:      Name,
	Type:      awgtypes.Type,
	Tool:      "awg",
	Quick:     "awg-quick",
	ConfigDir: "/etc/amnezia/amneziawg",
}

var (
	_ types.Service = (*AmneziaWG)(nil)

	// lookPath is how Init checks for the tools; a variable so tests can
	// stub it.
	lookPath = exec.LookPath

	// ensureForwarding turns IP forwarding on; a variable so tests can skip it.
	ensureForwarding = common.EnsureForwarding
)

// AmneziaWG is the default tier's WireGuard service plus, when the
// configuration enables it, the 3.1 tier's on a second interface with its
// own tunnel subnets. A peer's tunnel address says which tier it is on.
type AmneziaWG struct {
	*wireguard.WireGuard
	v3     *wireguard.WireGuard
	v3Net  *net.IPNet
	config *awgtypes.Config
	// preset pools, when set, replace the tunnel networks Init would take
	// from amneziawg.toml.
	pool, poolV3 *wgtypes.IPPool
}

// NewAmneziaWG builds the service. Nil pools, as a node uses, mean each tier
// gets the tunnel networks its configuration names or its key derives; tests
// pass fixed ones.
func NewAmneziaWG(pool, poolV3 *wgtypes.IPPool) *AmneziaWG {
	return &AmneziaWG{
		WireGuard: wireguard.NewVariant(Variant, pool),
		v3:        wireguard.NewVariant(Variant, poolV3),
		config:    awgtypes.NewConfig(),
		pool:      pool,
		poolV3:    poolV3,
	}
}

// NewService builds the AmneziaWG service for a node.
func NewService(_ *types.Config) (types.Service, error) {
	return NewAmneziaWG(nil, nil), nil
}

// usePools hands each tier its address pool: the preset ones, or pools over
// the tunnel networks of the configuration.
func (s *AmneziaWG) usePools() error {
	pool, poolV3 := s.pool, s.poolV3
	if pool == nil {
		def, v3, err := s.config.TunnelNetworks()
		if err != nil {
			return err
		}
		pool = def.Pool()
		if s.v3On() {
			poolV3 = v3.Pool()
		}
	}

	s.WireGuard.WithPool(pool)
	if poolV3 != nil {
		s.v3.WithPool(poolV3)
		s.v3Net = poolV3.V4.Net
	}

	return nil
}

// Command is the "amneziawg config …" CLI subtree.
func Command() *cobra.Command {
	return common.ConfigCommand(Name, []string{"awg"}, "AmneziaWG sub-commands", common.ConfigSpec{
		FileName: awgtypes.ConfigFileName,
		Default:  func() common.ConfigFile { return awgtypes.NewConfig().WithDefaultValues() },
		Read:     func(v *viper.Viper) (common.ConfigFile, error) { return awgtypes.ReadInConfig(v) },
	})
}

// Init reads amneziawg.toml, hands each tier's WireGuard part and parameter
// lines to its tunnel service, and lets them write the interface
// configurations.
func (s *AmneziaWG) Init(home string) (err error) {
	for _, tool := range []string{Variant.Tool, Variant.Quick} {
		if _, err = lookPath(tool); err != nil {
			return fmt.Errorf("the %q tool is not on PATH: install amneziawg-tools on the host "+
				"(docs/operator.md, section 6) or run the Docker image, which bundles it: %w", tool, err)
		}
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, awgtypes.ConfigFileName))

	s.config, err = awgtypes.ReadInConfig(v)
	if err != nil {
		return err
	}
	if err = s.config.Validate(); err != nil {
		return err
	}
	if err = s.usePools(); err != nil {
		return err
	}

	s.WireGuard.WithConfig(s.config.WireGuard(), s.config.Obfuscation.InterfaceLines())
	if err = s.WireGuard.Init(home); err != nil {
		return err
	}
	if !s.v3On() {
		return nil
	}

	s.v3.WithConfig(s.config.WireGuardV3(), s.config.V3.InterfaceLines(s.config.Obfuscation))

	return s.v3.Init(home)
}

// v3On says whether the 3.1 tier is offered.
func (s *AmneziaWG) v3On() bool {
	return s.config.V3Enabled()
}

// Start turns IP forwarding on, brings the default tier up, then the 3.1
// tier; if the second fails the first is taken down again, so a failed start
// leaves nothing behind.
func (s *AmneziaWG) Start() error {
	if err := ensureForwarding(); err != nil {
		return err
	}
	if err := s.WireGuard.Up(); err != nil {
		return err
	}
	if !s.v3On() {
		return nil
	}
	if err := s.v3.Up(); err != nil {
		_ = s.WireGuard.Stop()
		return err
	}

	return nil
}

// Stop takes both tiers down and reports the first failure.
func (s *AmneziaWG) Stop() error {
	err := s.WireGuard.Stop()
	if s.v3On() {
		if e := s.v3.Stop(); err == nil {
			err = e
		}
	}

	return err
}

// ParsePeerRequest reads WireGuard's {"public_key"} plus an optional
// "awg_version": absent or 2 asks for the default tier, 3 for the 3.1 tier.
// A tier this node does not offer is refused here, before admission. The
// peer data, and so the session key, stays the 32-byte public key exactly
// as for WireGuard; the tier travels with the request to AddPeerForRequest.
func (s *AmneziaWG) ParsePeerRequest(raw []byte) ([]byte, error) {
	key, err := s.WireGuard.ParsePeerRequest(raw)
	if err != nil {
		return nil, err
	}
	if _, err := s.tier(raw); err != nil {
		return nil, err
	}

	return key, nil
}

// tier is the tier a peer request asks for, refused when this node does not
// offer it.
func (s *AmneziaWG) tier(raw []byte) (int, error) {
	var req struct {
		Version *int `json:"awg_version"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return 0, fmt.Errorf("invalid %s peer request: %w", Name, err)
	}

	tier := awgtypes.VersionDefault
	if req.Version != nil {
		tier = *req.Version
	}

	switch {
	case tier == awgtypes.VersionDefault:
	case tier == awgtypes.Version3 && s.v3On():
	default:
		return 0, fmt.Errorf("unsupported awg_version %d; this node offers %s", tier, s.offered())
	}

	return tier, nil
}

// offered lists the awg_version values this node answers to.
func (s *AmneziaWG) offered() string {
	if s.v3On() {
		return fmt.Sprintf("%d and %d", awgtypes.VersionDefault, awgtypes.Version3)
	}

	return fmt.Sprintf("%d only", awgtypes.VersionDefault)
}

// AddPeerForRequest adds the peer to the tier its request asked for.
func (s *AmneziaWG) AddPeerForRequest(request, data []byte) ([]byte, error) {
	tier, err := s.tier(request)
	if err != nil {
		return nil, err
	}
	if tier == awgtypes.Version3 {
		return s.v3.AddPeer(data)
	}

	return s.WireGuard.AddPeer(data)
}

// AddPeer adds a peer that came without a request, from the legacy
// endpoint, to the default tier.
func (s *AmneziaWG) AddPeer(data []byte) ([]byte, error) {
	return s.WireGuard.AddPeer(data)
}

func (s *AmneziaWG) HasPeer(data []byte) bool {
	return s.WireGuard.HasPeer(data) || (s.v3On() && s.v3.HasPeer(data))
}

// RemovePeer removes the peer from the tier that has it; a peer neither has
// is removed from the default tier, which is not an error there.
func (s *AmneziaWG) RemovePeer(data []byte) error {
	if s.v3On() && s.v3.HasPeer(data) {
		return s.v3.RemovePeer(data)
	}

	return s.WireGuard.RemovePeer(data)
}

// Peers lists the peers of both tiers with their counters.
func (s *AmneziaWG) Peers() ([]types.Peer, error) {
	items, err := s.WireGuard.Peers()
	if err != nil || !s.v3On() {
		return items, err
	}

	more, err := s.v3.Peers()
	if err != nil {
		return nil, err
	}

	return append(items, more...), nil
}

func (s *AmneziaWG) PeerCount() int {
	count := s.WireGuard.PeerCount()
	if s.v3On() {
		count += s.v3.PeerCount()
	}

	return count
}

// PublicInbound is one AmneziaWG entry of the root document: everything a
// client needs comes with the handshake, so all of it is blank in public
// except the tier, which a client reads before asking for it.
type PublicInbound struct {
	Port       int     `json:"port"`
	PublicKey  *string `json:"public_key"`
	S1         uint16  `json:"s1"`
	S2         uint16  `json:"s2"`
	S3         uint16  `json:"s3"`
	S4         uint16  `json:"s4"`
	H1         uint32  `json:"h1"`
	H2         uint32  `json:"h2"`
	H3         uint32  `json:"h3"`
	H4         uint32  `json:"h4"`
	AWGVersion int     `json:"awg_version"`
}

// PublicMetadata lists one listener per tier with its details blanked.
func (s *AmneziaWG) PublicMetadata() interface{} {
	inbounds := []PublicInbound{{AWGVersion: awgtypes.VersionDefault}}
	if s.v3On() {
		inbounds = append(inbounds, PublicInbound{AWGVersion: awgtypes.Version3})
	}

	return inbounds
}

// PeerMetadata is WireGuard's endpoint details plus the parameters the
// client must match. Junk packet counts are per side and not sent.
type PeerMetadata struct {
	wireguard.PeerMetadata
	S1 uint16 `json:"s1"`
	S2 uint16 `json:"s2"`
	S3 uint16 `json:"s3"`
	S4 uint16 `json:"s4"`
	H1 uint32 `json:"h1"`
	H2 uint32 `json:"h2"`
	H3 uint32 `json:"h3"`
	H4 uint32 `json:"h4"`
	I1 string `json:"i1,omitempty"`
	I2 string `json:"i2,omitempty"`
	I3 string `json:"i3,omitempty"`
	I4 string `json:"i4,omitempty"`
	I5 string `json:"i5,omitempty"`
}

// HandshakePayloadData is what a default-tier client receives in
// result.data.
type HandshakePayloadData struct {
	Addrs    []string       `json:"addrs"`
	Metadata []PeerMetadata `json:"metadata"`
}

// PeerMetadataV3 is the 3.1 tier's entry: the same keys plus the tier, the
// header protection key, whether trailers are on and the tunnel MTU to use.
type PeerMetadataV3 struct {
	PeerMetadata
	AWGVersion          int    `json:"awg_version"`
	HeaderProtectionKey string `json:"header_protection_key"`
	RandomTrailers      bool   `json:"random_trailers"`
	MTU                 int    `json:"mtu"`
}

// HandshakePayloadDataV3 is what a 3.1-tier client receives in result.data.
type HandshakePayloadDataV3 struct {
	Addrs    []string         `json:"addrs"`
	Metadata []PeerMetadataV3 `json:"metadata"`
}

// HandshakePayload is WireGuard's payload with the obfuscation parameters
// added to the metadata entry; the tier is read off the assigned tunnel
// address.
func (s *AmneziaWG) HandshakePayload(result []byte) (interface{}, error) {
	if s.v3On() && len(result) >= net.IPv4len && s.v3Net.Contains(net.IP(result[:net.IPv4len])) {
		return s.handshakePayloadV3(result)
	}

	base, err := s.WireGuard.HandshakePayload(result)
	if err != nil {
		return nil, err
	}
	wg := base.(wireguard.HandshakePayloadData)

	o := s.config.Obfuscation
	entry := PeerMetadata{
		PeerMetadata: wg.Metadata[0],
		S1:           o.S1, S2: o.S2, S3: o.S3, S4: o.S4,
		H1: o.H1, H2: o.H2, H3: o.H3, H4: o.H4,
		I1: o.I1, I2: o.I2, I3: o.I3, I4: o.I4, I5: o.I5,
	}

	return HandshakePayloadData{Addrs: wg.Addrs, Metadata: []PeerMetadata{entry}}, nil
}

func (s *AmneziaWG) handshakePayloadV3(result []byte) (interface{}, error) {
	base, err := s.v3.HandshakePayload(result)
	if err != nil {
		return nil, err
	}
	wg := base.(wireguard.HandshakePayloadData)

	o, v := s.config.Obfuscation, s.config.V3
	entry := PeerMetadataV3{
		PeerMetadata: PeerMetadata{
			PeerMetadata: wg.Metadata[0],
			S1:           v.S1, S2: v.S2, S3: v.S3, S4: v.S4,
			H1: v.H1, H2: v.H2, H3: v.H3, H4: v.H4,
			I1: o.I1, I2: o.I2, I3: o.I3, I4: o.I4, I5: o.I5,
		},
		AWGVersion:          awgtypes.Version3,
		HeaderProtectionKey: v.HeaderProtectionKey,
		RandomTrailers:      v.RandomTrailers,
		MTU:                 awgtypes.V3MTU,
	}

	return HandshakePayloadDataV3{Addrs: wg.Addrs, Metadata: []PeerMetadataV3{entry}}, nil
}

// String renders the payload for logs and tests.
func (p HandshakePayloadData) String() string {
	b, _ := json.Marshal(p)

	return string(b)
}

// String renders the payload for logs and tests.
func (p HandshakePayloadDataV3) String() string {
	b, _ := json.Marshal(p)

	return string(b)
}
