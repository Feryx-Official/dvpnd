// SPDX-License-Identifier: Apache-2.0

package types

import (
	"crypto/sha256"
	"net"

	"github.com/pkg/errors"

	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
)

// Each tier hands its peers addresses from tunnel networks of its own. Nodes
// that all handed out 10.8.0.x, the subnet most WireGuard servers use, failed
// the network's health probe, and a client that already routes a network
// cannot bring up a tunnel that reuses it. Unless amneziawg.toml names them,
// a tier's networks are derived from its public key: the same across
// restarts, different from node to node.
const (
	// Derived IPv4 networks are 10.x.y.0/24 with x from 11 to 250. 10.0 to
	// 10.10 are left out: home and cloud networks crowd there, and so do the
	// subnets WireGuard and OpenVPN servers use by default.
	derivedIPv4Low    = 11
	derivedIPv4Span   = 240
	derivedIPv4Prefix = 24
	// Derived IPv6 networks are a unique local /120 (RFC 4193): fd, a 40-bit
	// global ID, subnet 0.
	derivedIPv6Prefix = 120

	derivationLabel = "dvpnd amneziawg tunnel networks\x00"
)

// Networks are a tier's IPv4 and IPv6 tunnel networks. The interface takes
// the first host of each, peers the ones after it.
type Networks struct {
	IPv4 *net.IPNet
	IPv6 *net.IPNet
}

// DeriveNetworks returns the networks a tier gets from its public key.
func DeriveNetworks(public []byte) Networks {
	sum := sha256.Sum256(append([]byte(derivationLabel), public...))

	v6 := make(net.IP, net.IPv6len)
	v6[0] = 0xfd
	copy(v6[1:6], sum[2:7])

	return Networks{
		IPv4: &net.IPNet{
			IP:   net.IPv4(10, derivedIPv4Low+sum[0]%derivedIPv4Span, sum[1], 0).To4(),
			Mask: net.CIDRMask(derivedIPv4Prefix, 8*net.IPv4len),
		},
		IPv6: &net.IPNet{IP: v6, Mask: net.CIDRMask(derivedIPv6Prefix, 8*net.IPv6len)},
	}
}

// Pool is the address pool the networks hand peers: the host after the
// interface's own, onwards.
func (n Networks) Pool() *wgtypes.IPPool {
	v4 := wgtypes.NewIPv4FromIP(n.IPv4.IP).Next().Next()
	v6 := wgtypes.NewIPv6FromIP(n.IPv6.IP).Next().Next()

	return wgtypes.NewIPPool(
		wgtypes.NewIPv4Pool(v4.IP(), n.IPv4),
		wgtypes.NewIPv6Pool(v6.IP(), n.IPv6),
	)
}

// Overlaps says whether either family's networks share addresses.
func (n Networks) Overlaps(o Networks) bool {
	return overlap(n.IPv4, o.IPv4) || overlap(n.IPv6, o.IPv6)
}

func overlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// tierNetworks is a tier's networks: the subnets its settings name, the
// derived ones where a setting is empty.
func tierNetworks(ipv4, ipv6, privateKey string) (Networks, error) {
	key, err := wgtypes.KeyFromString(privateKey)
	if err != nil {
		return Networks{}, errors.Wrap(err, "invalid private_key")
	}
	n := DeriveNetworks(key.Public().Bytes())

	if ipv4 != "" {
		if n.IPv4, err = parseSubnet(ipv4, false); err != nil {
			return Networks{}, errors.Wrap(err, "invalid ipv4_subnet")
		}
	}
	if ipv6 != "" {
		if n.IPv6, err = parseSubnet(ipv6, true); err != nil {
			return Networks{}, errors.Wrap(err, "invalid ipv6_subnet")
		}
	}

	return n, nil
}

// parseSubnet reads a subnet setting: a private network in CIDR form, /16 to
// /28 for IPv4 and /64 to /124 for IPv6, so there is room for peers but not a
// whole address space.
func parseSubnet(s string, v6 bool) (*net.IPNet, error) {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}
	ones, _ := n.Mask.Size()

	switch {
	case !v6 && ip.To4() == nil:
		return nil, errors.Errorf("%s is not an IPv4 network", s)
	case v6 && ip.To4() != nil:
		return nil, errors.Errorf("%s is not an IPv6 network", s)
	case !ip.IsPrivate():
		return nil, errors.Errorf("%s is not a private network", s)
	case !v6 && (ones < 16 || ones > 28):
		return nil, errors.Errorf("%s: the prefix must be /16 to /28", s)
	case v6 && (ones < 64 || ones > 124):
		return nil, errors.Errorf("%s: the prefix must be /64 to /124", s)
	}

	return n, nil
}
