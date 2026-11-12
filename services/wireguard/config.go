// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package wireguard

import (
	"strings"
)

// The FORWARD rules accept traffic leaving the tunnel and the replies coming
// back into it. The second rule matters on a host whose FORWARD policy is DROP
// (ufw, or a host where Docker is or was running): without it every reply to a
// peer is dropped and TCP through the tunnel hangs. Every call waits for the
// xtables lock, as the node's own do (services/common/host.go).
//
// nolint:lll
var (
	configTemplate = strings.TrimSpace(`
[Interface]
Address = {{ .Address }}
ListenPort = {{ .ListenPort }}
PrivateKey = {{ .PrivateKey }}
{{- range .Extra }}
{{ . }}
{{- end }}
PostUp = iptables -w 5 -A FORWARD -i %i -j ACCEPT; iptables -w 5 -A FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -w 5 -t nat -A POSTROUTING -o {{ .Uplink }} -j MASQUERADE; ip6tables -w 5 -A FORWARD -i %i -j ACCEPT; ip6tables -w 5 -A FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; ip6tables -w 5 -t nat -A POSTROUTING -o {{ .Uplink }} -j MASQUERADE;
PostDown = iptables -w 5 -D FORWARD -i %i -j ACCEPT; iptables -w 5 -D FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -w 5 -t nat -D POSTROUTING -o {{ .Uplink }} -j MASQUERADE; ip6tables -w 5 -D FORWARD -i %i -j ACCEPT; ip6tables -w 5 -D FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; ip6tables -w 5 -t nat -D POSTROUTING -o {{ .Uplink }} -j MASQUERADE;
    `)
)
