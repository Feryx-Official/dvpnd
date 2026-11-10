// SPDX-License-Identifier: Apache-2.0

package hysteria

import (
	"encoding/binary"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	hysteriatypes "github.com/Feryx-Official/dvpnd/v9/services/hysteria/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// Name is the service_type string clients expect.
const Name = "hysteria2"

// NewService builds the Hysteria2 service for a node.
func NewService(_ *types.Config) (types.Service, error) {
	return NewHysteria(), nil
}

// Command is the "hysteria2 config …" CLI subtree.
func Command() *cobra.Command {
	return common.ConfigCommand(Name, []string{"hysteria", "hy2"}, "Hysteria2 sub-commands", common.ConfigSpec{
		FileName: hysteriatypes.ConfigFileName,
		Default:  func() common.ConfigFile { return hysteriatypes.NewConfig().WithDefaultValues() },
		Read:     func(v *viper.Viper) (common.ConfigFile, error) { return hysteriatypes.ReadInConfig(v) },
	})
}

func (s *Hysteria) Name() string {
	return Name
}

// ListenPort is the port encoded in the first two bytes of Info().
func (s *Hysteria) ListenPort() uint16 {
	return binary.BigEndian.Uint16(s.info[:2])
}

// ParsePeerRequest reads {"uuid": …}. Client apps send the canonical string
// for this node type (it doubles as the password they present); a 16-byte
// array is accepted too. The peer data is the 16 raw bytes.
func (s *Hysteria) ParsePeerRequest(raw []byte) ([]byte, error) {
	id, err := common.UUIDPeerRequest(s.Name(), raw)
	if err != nil {
		return nil, err
	}

	return id[:], nil
}

// HandshakePayloadData is what a Hysteria2 client receives in result.data.
type HandshakePayloadData struct {
	Metadata []Inbound `json:"metadata"`
}

// Inbound is the Hysteria2 handshake entry: exactly the keys, and the pin
// format, that Hysteria2 nodes on the network send. The network's health
// probe failed the handshake while the entry carried three more keys and a
// plain-hex pin.
type Inbound struct {
	Port         uint16 `json:"port"`
	TLSPin       string `json:"tls_pin"`
	ObfsPassword string `json:"obfs_password"`
}

// HandshakePayload gives the client the port, the certificate pin it must
// verify, and the obfuscation password (empty when obfuscation is off).
func (s *Hysteria) HandshakePayload(_ []byte) (interface{}, error) {
	return HandshakePayloadData{Metadata: []Inbound{{
		Port:         s.ListenPort(),
		TLSPin:       s.tlsPin,
		ObfsPassword: s.config.Server.ObfsPassword,
	}}}, nil
}

// PublicInbound is the Hysteria2 entry of the root document: the port and
// pin come with the handshake, and the obfuscation password is only shown to
// exist, as nodes on the network publish it.
type PublicInbound struct {
	Port         int    `json:"port"`
	TLSPin       string `json:"tls_pin"`
	ObfsPassword string `json:"obfs_password"`
}

// PublicMetadata lists the QUIC listener with its details blanked.
func (s *Hysteria) PublicMetadata() interface{} {
	entry := PublicInbound{}
	if s.config != nil && s.config.Server.ObfsPassword != "" {
		entry.ObfsPassword = "<redacted>"
	}

	return []PublicInbound{entry}
}
