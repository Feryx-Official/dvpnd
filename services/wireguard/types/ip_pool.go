// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

import (
	"net"
	"sync"

	"github.com/pkg/errors"
)

type IPv4Pool struct {
	Net       *net.IPNet
	current   IPv4
	available []IPv4
	reserved  map[IPv4]bool
	mutex     *sync.Mutex
}

func NewIPv4Pool(ip net.IP, ipNet *net.IPNet) *IPv4Pool {
	return &IPv4Pool{
		Net:      ipNet,
		current:  NewIPv4FromIP(ip),
		reserved: make(map[IPv4]bool),
		mutex:    &sync.Mutex{},
	}
}

func (p *IPv4Pool) Get() (ip IPv4, err error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.available) == 0 {
		// The network and broadcast addresses are no host's. An address
		// ending in .0 or .255 inside a network larger than a /24 is a
		// host's, but is left out as it always was.
		network, last := bounds(p.Net)
		for p.current[3] == 0 || p.current[3] == 255 || p.current == network || p.current == last {
			p.current = p.current.Next()
		}
		if !p.Net.Contains(p.current.IP()) {
			return ip, errors.New("ipv4 pool is full")
		}

		ip, p.current = p.current, p.current.Next()
	} else {
		ip, p.available = p.available[0], p.available[1:]
	}

	p.reserved[ip] = true
	return ip, nil
}

// bounds are the network address of n and its broadcast address.
func bounds(n *net.IPNet) (network, last IPv4) {
	ip, mask := n.IP.To4(), n.Mask
	if len(mask) == net.IPv6len {
		mask = mask[net.IPv6len-net.IPv4len:]
	}
	for i := range network {
		network[i] = ip[i] & mask[i]
		last[i] = ip[i] | ^mask[i]
	}

	return network, last
}

func (p *IPv4Pool) Release(ip IPv4) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.reserved[ip] {
		delete(p.reserved, ip)
		p.available = append(p.available, ip)
	}
}

func NewIPv4PoolFromCIDR(s string) (*IPv4Pool, error) {
	ip, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}

	return NewIPv4Pool(ip, ipNet), nil
}

type IPv6Pool struct {
	Net       *net.IPNet
	current   IPv6
	available []IPv6
	reserved  map[IPv6]bool
	mutex     *sync.Mutex
}

func NewIPv6Pool(ip net.IP, ipNet *net.IPNet) *IPv6Pool {
	return &IPv6Pool{
		Net:      ipNet,
		current:  NewIPv6FromIP(ip),
		reserved: make(map[IPv6]bool),
		mutex:    &sync.Mutex{},
	}
}

func (p *IPv6Pool) Get() (ip IPv6, err error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.available) == 0 {
		if !p.Net.Contains(p.current.IP()) {
			return ip, errors.New("ipv6 pool is full")
		}

		ip, p.current = p.current, p.current.Next()
	} else {
		ip, p.available = p.available[0], p.available[1:]
	}

	p.reserved[ip] = true
	return ip, nil
}

func (p *IPv6Pool) Release(ip IPv6) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.reserved[ip] {
		delete(p.reserved, ip)
		p.available = append(p.available, ip)
	}
}

func NewIPv6PoolFromCIDR(s string) (*IPv6Pool, error) {
	ip, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}

	return NewIPv6Pool(ip, ipNet), nil
}

type IPPool struct {
	V4 *IPv4Pool
	V6 *IPv6Pool
}

func NewIPPool(v4 *IPv4Pool, v6 *IPv6Pool) *IPPool {
	return &IPPool{
		V4: v4,
		V6: v6,
	}
}

func (p *IPPool) Get() (IPv4, IPv6, error) {
	v4, err := p.V4.Get()
	if err != nil {
		return IPv4{}, IPv6{}, err
	}

	v6, err := p.V6.Get()
	if err != nil {
		p.V4.Release(v4)

		return IPv4{}, IPv6{}, err
	}

	return v4, v6, nil
}

func (p *IPPool) Release(v4 IPv4, v6 IPv6) {
	p.V4.Release(v4)
	p.V6.Release(v6)
}
