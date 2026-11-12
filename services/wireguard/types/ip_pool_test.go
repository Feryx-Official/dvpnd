// SPDX-License-Identifier: Apache-2.0

package types

import (
	"net"
	"strings"
	"sync"
	"testing"
)

// TestIPv4PoolHandsOutEachAddressOnce: every peer gets an address of its
// own, never the node's (the first host) nor the network or broadcast
// address; a full pool refuses instead of wrapping round to an address in
// use, and a released address is handed out again.
//
// Rules: [CT-9].
func TestIPv4PoolHandsOutEachAddressOnce(t *testing.T) {
	pool, err := NewIPv4PoolFromCIDR("10.8.0.2/24")
	if err != nil {
		t.Fatal(err)
	}
	node := net.ParseIP("10.8.0.1")

	seen := map[string]bool{}
	for {
		ip, err := pool.Get()
		if err != nil {
			break
		}
		addr := ip.IP()
		switch {
		case seen[addr.String()]:
			t.Fatalf("%s handed out twice", addr)
		case addr.Equal(node):
			t.Fatalf("the node's own address %s handed out", addr)
		case addr.To4()[3] == 0 || addr.To4()[3] == 255:
			t.Fatalf("the network or broadcast address %s handed out", addr)
		case !pool.Net.Contains(addr):
			t.Fatalf("%s is outside %s", addr, pool.Net)
		}
		seen[addr.String()] = true
	}
	if len(seen) != 253 {
		t.Fatalf("a /24 handed out %d addresses, want 253 (.2 to .254)", len(seen))
	}
	if _, err := pool.Get(); err == nil {
		t.Fatal("a full pool handed out another address")
	}

	back := NewIPv4FromIP(net.ParseIP("10.8.0.77"))
	pool.Release(back)
	if ip, err := pool.Get(); err != nil || !ip.IP().Equal(back.IP()) {
		t.Fatalf("after releasing %s: got %v, %v", back.IP(), ip.IP(), err)
	}
}

// Rules: [CT-9].
func TestIPv6PoolHandsOutEachAddressOnce(t *testing.T) {
	pool, err := NewIPv6PoolFromCIDR("fd86:ea04:1115::2/120")
	if err != nil {
		t.Fatal(err)
	}
	node := net.ParseIP("fd86:ea04:1115::1")

	seen := map[string]bool{}
	for {
		ip, err := pool.Get()
		if err != nil {
			break
		}
		addr := ip.IP()
		if seen[addr.String()] || addr.Equal(node) || !pool.Net.Contains(addr) {
			t.Fatalf("%s: handed out twice, the node's own, or outside %s", addr, pool.Net)
		}
		seen[addr.String()] = true
	}
	if len(seen) != 254 {
		t.Fatalf("a /120 handed out %d addresses, want 254 (::2 to ::ff)", len(seen))
	}
	if _, err := pool.Get(); err == nil {
		t.Fatal("a full pool handed out another address")
	}
}

// TestPoolUnderConcurrentHandshakes: peers added at the same time still get
// distinct addresses.
//
// Rules: [CT-9].
func TestPoolUnderConcurrentHandshakes(t *testing.T) {
	v4, _ := NewIPv4PoolFromCIDR("10.8.0.2/24")
	v6, _ := NewIPv6PoolFromCIDR("fd86:ea04:1115::2/120")
	pool := NewIPPool(v4, v6)

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		seen = map[string]bool{}
	)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, b, err := pool.Get()
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range []string{a.IP().String(), b.IP().String()} {
				if seen[s] {
					t.Errorf("%s handed out twice", s)
				}
				seen[s] = true
			}
		}()
	}
	wg.Wait()
}

// TestPoolReleasesIPv4WhenIPv6IsFull: when the IPv6 pool is full the IPv4
// address taken for the peer goes back, rather than leaking until the pool
// runs dry.
//
// Rules: [CT-9].
func TestPoolReleasesIPv4WhenIPv6IsFull(t *testing.T) {
	v4, _ := NewIPv4PoolFromCIDR("10.8.0.2/24")
	v6, _ := NewIPv6PoolFromCIDR("fd00::2/127") // two addresses
	pool := NewIPPool(v4, v6)

	for i := 0; i < 2; i++ {
		if _, _, err := pool.Get(); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := pool.Get(); err == nil {
		t.Fatal("the IPv6 pool is full, yet a pair was handed out")
	}
	if ip, err := v4.Get(); err != nil || !ip.IP().Equal(net.ParseIP("10.8.0.4")) {
		t.Fatalf("the IPv4 address of the failed pair was not released: next is %v, %v", ip.IP(), err)
	}
}

// TestIPv4PoolKeepsOffTheBroadcastAddress: in a subnet of any size the
// operator may set (AmneziaWG takes /16 to /28) no peer gets the network or
// the broadcast address, and a full pool says so.
//
// Rules: [CT-9].
func TestIPv4PoolKeepsOffTheBroadcastAddress(t *testing.T) {
	for _, tc := range []struct {
		cidr string
		want int
	}{
		{"10.20.30.18/28", 13}, // .18 to .30
		{"10.20.30.66/26", 61}, // .66 to .126
		{"10.20.30.2/23", 507}, // .30.2 to .31.254, without .30.255 and .31.0
	} {
		pool, err := NewIPv4PoolFromCIDR(tc.cidr)
		if err != nil {
			t.Fatal(err)
		}
		ones, _ := pool.Net.Mask.Size()
		network := pool.Net.IP.To4()
		last := make(net.IP, 4)
		for i := range last {
			last[i] = network[i] | ^net.CIDRMask(ones, 32)[i]
		}

		n := 0
		for {
			ip, err := pool.Get()
			if err != nil {
				if !strings.Contains(err.Error(), "full") {
					t.Errorf("%s: %v", tc.cidr, err)
				}
				break
			}
			if a := ip.IP(); a.Equal(network) || a.Equal(last) || !pool.Net.Contains(a) {
				t.Fatalf("%s: handed out %s", tc.cidr, a)
			}
			n++
		}
		if n != tc.want {
			t.Errorf("%s handed out %d addresses, want %d", tc.cidr, n, tc.want)
		}
	}
}
