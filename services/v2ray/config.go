// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package v2ray

import (
	"strings"

	v2raytypes "github.com/Feryx-Official/dvpnd/v9/services/v2ray/types"
)

// configTemplate is the v2ray server configuration the node writes at start.
// The control API listens on a loopback port of the node's choosing. The
// node's egress policy is routed to a blackhole: the blocked networks,
// localhost by name, and TCP port 25 unless allowed. IPIfNonMatch resolves a
// destination given as a name and matches its addresses too, so a name that
// points at loopback cannot reach the control API. Every string is written
// through json, so a value reads back as itself.
var (
	configTemplate = strings.TrimSpace(`
{
    "api": {
        "services": [
            "HandlerService",
            "StatsService"
        ],
        "tag": "api"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": {{ .API.Port }},
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        },
        {
            "port": "{{ .VMess.ListenPort }}",
            "protocol": "vmess",
            "streamSettings": {
                "network": {{ json .VMess.Transport }},
                "security": {{ json .VMess.Security }},
                "tlsSettings": {
                    "allowInsecure": true,
                    "certificates": [
                        {
                            "certificateFile": {{ json .VMess.TLSCertPath }},
                            "keyFile": {{ json .VMess.TLSKeyPath }}
                        }
                    ]
                }
            },
            "tag": "vmess"
        }
    ],
    "log": {
        "access": "none",
        "error": "none",
        "loglevel": "none"
    },
    "outbounds": [
        {
            "protocol": "freedom",
            "tag": "direct"
        },
        {
            "protocol": "blackhole",
            "tag": "blocked"
        }
    ],
    "policy": {
        "levels": {
            "0": {
                "statsUserDownlink": true,
                "statsUserUplink": true
            }
        }
    },
    "routing": {
        "domainStrategy": "IPIfNonMatch",
        "rules": [
            {
                "inboundTag": [
                    "api"
                ],
                "outboundTag": "api",
                "type": "field"
            },
            {
                "domain": [
                    {{ json (print "domain:" .BlockedDomain) }}
                ],
                "outboundTag": "blocked",
                "type": "field"
            },
{{- if not .AllowSMTP }}
            {
                "network": "tcp",
                "outboundTag": "blocked",
                "port": "{{ .SMTPPort }}",
                "type": "field"
            },
{{- end }}
            {
                "ip": {{ json .BlockedNetworks }},
                "outboundTag": "blocked",
                "type": "field"
            }
        ]
    },
    "stats": {},
    "transport": {
        "dsSettings": {},
        "grpcSettings": {},
        "gunSettings": {},
        "httpSettings": {},
        "kcpSettings": {},
        "quicSettings": {
            "security": "chacha20-poly1305"
        },
        "tcpSettings": {},
        "wsSettings": {}
    }
}
	`)
)

// templateData is the configuration plus the egress policy (see
// common.Egress).
type templateData struct {
	*v2raytypes.Config

	BlockedNetworks []string
	BlockedDomain   string
	SMTPPort        int
	AllowSMTP       bool
}
