// SPDX-License-Identifier: Apache-2.0

package wireguard

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// Name is the service_type string clients expect.
const Name = "wireguard"

// NewService builds the WireGuard service for a node, with the tunnel address
// pools the node hands out.
func NewService(_ *types.Config) (types.Service, error) {
	ipv4Pool, err := wgtypes.NewIPv4PoolFromCIDR(types.IPv4CIDR)
	if err != nil {
		return nil, err
	}

	ipv6Pool, err := wgtypes.NewIPv6PoolFromCIDR(types.IPv6CIDR)
	if err != nil {
		return nil, err
	}

	return NewWireGuard(wgtypes.NewIPPool(ipv4Pool, ipv6Pool)), nil
}

// Command is the "wireguard config …" CLI subtree.
func Command() *cobra.Command {
	return common.ConfigCommand(Name, []string{"wg"}, "WireGuard sub-commands", common.ConfigSpec{
		FileName: wgtypes.ConfigFileName,
		Default:  func() common.ConfigFile { return wgtypes.NewConfig().WithDefaultValues() },
		Read:     func(v *viper.Viper) (common.ConfigFile, error) { return wgtypes.ReadInConfig(v) },
	})
}

func (s *WireGuard) Name() string {
	return s.variant.Name
}

// ListenPort is the port encoded in the first two bytes of Info().
func (s *WireGuard) ListenPort() uint16 {
	return binary.BigEndian.Uint16(s.info[:2])
}

// PublicKey is the interface public key from Info(), base64.
func (s *WireGuard) PublicKey() string {
	return base64.StdEncoding.EncodeToString(s.info[2:])
}

// ParsePeerRequest reads {"public_key": <base64>} and returns the 32 raw bytes.
// A rejection names the protocol this node speaks, because the usual cause is a
// client built for another one: a proxy client sends a uuid instead.
func (s *WireGuard) ParsePeerRequest(raw []byte) ([]byte, error) {
	var req struct {
		PublicKey *string         `json:"public_key"`
		UUID      json.RawMessage `json:"uuid"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("invalid %s peer request: %w", s.Name(), err)
	}

	if req.PublicKey == nil {
		if len(req.UUID) > 0 {
			return nil, fmt.Errorf("this node runs %s, whose peer request carries a public_key; "+
				"this request carries uuid, which is what a proxy client sends", s.Name())
		}

		return nil, fmt.Errorf("this node runs %s, whose peer request carries a public_key; none was sent", s.Name())
	}

	key, err := wgtypes.KeyFromString(*req.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid public_key: %w", err)
	}

	return key.Bytes(), nil
}

// PeerMetadata is the per-node part of the WireGuard handshake payload.
type PeerMetadata struct {
	Port      uint16 `json:"port"`
	PublicKey string `json:"public_key"`
}

// HandshakePayloadData is what a WireGuard client receives in result.data.
type HandshakePayloadData struct {
	Addrs    []string       `json:"addrs"`
	Metadata []PeerMetadata `json:"metadata"`
}

// HandshakePayload turns AddPeer's result into the client's tunnel addresses
// and the node's endpoint details.
func (s *WireGuard) HandshakePayload(result []byte) (interface{}, error) {
	addrs, err := peerAddrs(result)
	if err != nil {
		return nil, err
	}

	return HandshakePayloadData{
		Addrs:    addrs,
		Metadata: []PeerMetadata{{Port: s.ListenPort(), PublicKey: s.PublicKey()}},
	}, nil
}

// PublicInbound is the WireGuard entry of the root document: the port and
// key are handed out per session, so both are blank in public.
type PublicInbound struct {
	Port      int     `json:"port"`
	PublicKey *string `json:"public_key"`
}

// PublicMetadata lists the single listener with its details blanked.
func (s *WireGuard) PublicMetadata() interface{} {
	return []PublicInbound{{}}
}

// peerAddrs turns AddPeer's result (4-byte IPv4 followed by 16-byte IPv6)
// into the tunnel addresses the client configures. An all-zero IPv6 means the
// node runs the tunnel IPv4-only and the client must not get an IPv6 address.
func peerAddrs(result []byte) ([]string, error) {
	if len(result) != 4+16 {
		return nil, fmt.Errorf("unexpected wireguard peer result length %d", len(result))
	}

	addrs := []string{net.IP(result[:4]).String() + "/32"}
	if v6 := net.IP(result[4:20]); !v6.Equal(net.IPv6zero) {
		addrs = append(addrs, v6.String()+"/128")
	}

	return addrs, nil
}
