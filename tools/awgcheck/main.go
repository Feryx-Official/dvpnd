// SPDX-License-Identifier: Apache-2.0

// awgcheck brings the AmneziaWG service up the way a node does, without a
// chain: it writes amneziawg.toml with both tiers, adds one peer per tier and
// writes the awg-quick configuration each peer's client needs, then prints
// the peers' counters until stopped. A verification tool, not part of the
// node: tools/awgcheck/check.sh runs it in a container against two client
// containers.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/services/amneziawg"
	awgtypes "github.com/Feryx-Official/dvpnd/v9/services/amneziawg/types"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

func main() {
	home := flag.String("home", "/out/home", "node home directory")
	out := flag.String("out", "/out", "where the client configurations go")
	endpoint := flag.String("endpoint", "172.30.0.10", "the address clients dial")
	v3 := flag.Bool("v3", true, "offer the 3.1 tier")
	flag.Parse()

	if err := run(*home, *out, *endpoint, *v3); err != nil {
		fmt.Fprintln(os.Stderr, "awgcheck:", err)
		os.Exit(1)
	}
}

func run(home, out, endpoint string, v3 bool) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}

	cfg := awgtypes.NewConfig().WithDefaultValues()
	cfg.ListenPort = 51820
	cfg.V3.ListenPort = 51821
	cfg.V3.Enabled = v3
	cfg.Obfuscation.I1 = "<b 0xabcd><r 8>"
	if err := cfg.SaveToPath(filepath.Join(home, awgtypes.ConfigFileName)); err != nil {
		return err
	}

	svc, err := amneziawg.NewService(nil)
	if err != nil {
		return err
	}
	s := svc.(*amneziawg.AmneziaWG)
	if err := s.Init(home); err != nil {
		return err
	}
	if err := s.Start(); err != nil {
		return err
	}
	defer func() {
		fmt.Println("stopping:", s.Stop())
	}()

	// Each tier's gateway is the first host of its tunnel network, which the
	// node derives from the tier's key; the clients ping it through the tunnel.
	def, netV3, err := cfg.TunnelNetworks()
	if err != nil {
		return err
	}
	gateways := map[int]string{2: gateway(def)}
	tiers := []int{2}
	if v3 {
		gateways[3] = gateway(netV3)
		tiers = append(tiers, 3)
	}
	for tier, gw := range gateways {
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("gateway%d", tier)), []byte(gw), 0o644); err != nil { //nolint:gosec // G306: check.sh reads it as the user who ran it, not root
			return err
		}
	}
	for _, tier := range tiers {
		priv, err := wgtypes.NewPrivateKey()
		if err != nil {
			return err
		}
		req := fmt.Sprintf(`{"public_key":%q}`, priv.Public().String())
		if tier == 3 {
			req = fmt.Sprintf(`{"public_key":%q,"awg_version":3}`, priv.Public().String())
		}
		data, err := s.ParsePeerRequest([]byte(req))
		if err != nil {
			return err
		}
		res, err := types.AddPeer(s, []byte(req), data)
		if err != nil {
			return err
		}
		payload, err := s.HandshakePayload(res)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(payload)
		fmt.Printf("tier %d payload: %s\n", tier, raw)

		conf, err := clientConf(raw, priv.String(), endpoint, gateways)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("client%d.conf", tier)), []byte(conf), 0o600); err != nil {
			return err
		}
	}
	fmt.Println("ready")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	t := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-sig:
			return nil
		case <-t.C:
			peers, err := s.Peers()
			if err != nil {
				return err
			}
			for _, p := range peers {
				fmt.Printf("peer %s up %d down %d\n", p.Key[:8], p.Upload, p.Download)
			}
		}
	}
}

// gateway is the node's own address on a tier's IPv4 tunnel network.
func gateway(n awgtypes.Networks) string {
	return wgtypes.NewIPv4FromIP(n.IPv4.IP).Next().IP().String()
}

// clientConf writes the awg-quick configuration a client app would, from the
// handshake payload: its own junk counts, the node's prefixes, headers and
// signature packets, and for the 3.1 tier the key, trailers and MTU.
func clientConf(payload []byte, privateKey, endpoint string, gateways map[int]string) (string, error) {
	var p struct {
		Addrs    []string                 `json:"addrs"`
		Metadata []map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	m := p.Metadata[0]
	num := func(k string) string {
		v, _ := m[k].(float64)
		return fmt.Sprintf("%.0f", v)
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\nAddress = %s\n", privateKey, strings.Join(p.Addrs, ", "))
	b.WriteString("Jc = 5\nJmin = 64\nJmax = 256\n")
	for _, k := range []string{"s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4"} {
		fmt.Fprintf(&b, "%s = %s\n", strings.ToUpper(k), num(k))
	}
	for _, k := range []string{"i1", "i2", "i3", "i4", "i5"} {
		if v, ok := m[k].(string); ok && v != "" {
			fmt.Fprintf(&b, "%s = %s\n", strings.ToUpper(k), v)
		}
	}
	if v, ok := m["awg_version"].(float64); ok && v == 3 {
		trailers := "off"
		if on, _ := m["random_trailers"].(bool); on {
			trailers = "on"
		}
		fmt.Fprintf(&b, "MTU = %s\nHeaderProtectionKey = %s\nRandomTrailers = %s\nContentPaddingAddition = 0-32\n",
			num("mtu"), m["header_protection_key"], trailers)
	}
	allowed := []string{"1.1.1.1/32"}
	for _, gw := range gateways {
		allowed = append(allowed, gw+"/32")
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nAllowedIPs = %s\nEndpoint = %s:%s\nPersistentKeepalive = 15\n",
		m["public_key"], strings.Join(allowed, ", "), endpoint, num("port"))

	return b.String(), nil
}
