// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package wireguard

import (
	"strings"
)

// PostUp and PostDown install and remove the tunnel's forwarding and
// masquerading rules, rendered from the rule set OpenVPN installs itself
// (services/common/nat.go).
var (
	configTemplate = marker + "\n" + strings.TrimSpace(`
[Interface]
Address = {{ .Address }}
ListenPort = {{ .ListenPort }}
PrivateKey = {{ .PrivateKey }}
{{- range .Extra }}
{{ . }}
{{- end }}
PostUp = {{ .PostUp }}
PostDown = {{ .PostDown }}
    `)
)
