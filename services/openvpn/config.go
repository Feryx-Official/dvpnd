// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"strings"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
)

// configTemplate is the server configuration the node writes at start. TLS
// settings match what the client apps' profile pins: ECDSA certificates,
// AES-GCM data channel, SHA256 HMAC, tls-crypt. Clients are admitted over the
// management interface (management-client-auth), not by a script; their
// credential is the certificate, so no username or password is demanded.
var configTemplate = strings.TrimSpace(`
dev {{ .Interface }}
dev-type tun
proto {{ if eq .Proto "tcp" }}tcp-server{{ else }}udp{{ end }}
port {{ .ListenPort }}
server {{ .IPv4Network }} {{ .IPv4Netmask }}
{{- if .EnableIPv6 }}
server-ipv6 {{ .IPv6Network }}
{{- end }}
topology subnet
ca {{ .CACert }}
cert {{ .ServerCert }}
key {{ .ServerKey }}
dh none
tls-crypt {{ .TLSCrypt }}
tls-server
tls-version-min 1.2
auth SHA256
data-ciphers AES-256-GCM:AES-128-GCM
data-ciphers-fallback AES-256-GCM
tls-cipher TLS-ECDHE-ECDSA-WITH-AES-256-GCM-SHA384
remote-cert-tls client
keepalive 10 60
persist-key
persist-tun
{{- if eq .Proto "udp" }}
explicit-exit-notify 1
{{- end }}
management {{ .ManagementSocket }} unix
management-client-user root
management-client-auth
auth-user-pass-optional
verb {{ .Verb }}
{{- if .User }}
user {{ .User }}
group {{ .Group }}
{{- end }}
`) + "\n"

// templateData is what the server configuration renders from.
type templateData struct {
	Interface   string
	Proto       string
	ListenPort  uint16
	EnableIPv6  bool
	IPv4Network string
	IPv4Netmask string
	IPv6Network string
	CACert      string
	ServerCert  string
	ServerKey   string
	TLSCrypt    string
	// ManagementSocket is the unix socket of the management interface, in
	// the runtime directory; only root may connect to it.
	ManagementSocket string
	Verb             int
	// User and Group, when set, are the account the server drops to once
	// its tunnel is up.
	User  string
	Group string
}

// verb is the OpenVPN log level: 3 (its usual level, which logs every client's
// address as it connects) only when the node runs at debug, otherwise 0, which
// leaves fatal errors only. Connects and disconnects reach the node over the
// management interface either way.
func verb() int {
	if common.Verbose() {
		return 3
	}

	return 0
}
