// SPDX-License-Identifier: Apache-2.0

package xray

import (
	"strings"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
)

// configTemplate is the xray server configuration the node writes at start.
// One VLESS inbound over raw TCP, wrapped in TLS with the node's certificate
// or in REALITY; peers are added and removed over the gRPC API on the
// loopback inbound, and per-user traffic statistics are switched on.
// The node's egress policy is routed to a blackhole: the blocked networks
// (listed explicitly, not with geoip:private, which needs the geoip asset
// file), localhost by name, and TCP port 25 unless allowed. IPIfNonMatch
// resolves a destination given as a name and matches its addresses too, so a
// name that points at loopback cannot reach the control API. Every string is
// written through json, so a value reads back as itself.
var configTemplate = strings.TrimSpace(`
{
    "log": {
        "access": "none",
        "loglevel": {{ json .LogLevel }}
    },
    "api": {
        "tag": "api",
        "services": [
            "HandlerService",
            "StatsService"
        ]
    },
    "stats": {},
    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true
            }
        }
    },
    "inbounds": [
        {
            "tag": "api",
            "listen": "127.0.0.1",
            "port": {{ .API.Port }},
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            }
        },
        {
            "tag": "vless",
            "port": {{ .VLESS.ListenPort }},
            "protocol": "vless",
            "settings": {
                "clients": [],
                "decryption": "none"
            },
            "streamSettings": {
                "network": "tcp",
                "security": {{ json .VLESS.Security }},
{{- if eq .VLESS.Security "tls" }}
                "tlsSettings": {
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "certificateFile": {{ json .TLSCertPath }},
                            "keyFile": {{ json .TLSKeyPath }}
                        }
                    ]
                }
{{- else }}
                "realitySettings": {
                    "show": false,
                    "dest": {{ json .Reality.Dest }},
                    "xver": 0,
                    "serverNames": [
                        {{ json .Reality.ServerName }}
                    ],
                    "privateKey": {{ json .Reality.PrivateKey }},
                    "shortIds": [
                        {{ json .Reality.ShortID }}
                    ]
                }
{{- end }}
            }
        }
    ],
    "outbounds": [
        {
            "tag": "direct",
            "protocol": "freedom"
        },
        {
            "tag": "blocked",
            "protocol": "blackhole"
        }
    ],
    "routing": {
        "domainStrategy": "IPIfNonMatch",
        "rules": [
            {
                "type": "field",
                "inboundTag": [
                    "api"
                ],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "domain": [
                    {{ json (print "domain:" .BlockedDomain) }}
                ],
                "outboundTag": "blocked"
            },
{{- if not .AllowSMTP }}
            {
                "type": "field",
                "network": "tcp",
                "port": "{{ .SMTPPort }}",
                "outboundTag": "blocked"
            },
{{- end }}
            {
                "type": "field",
                "ip": {{ json .BlockedNetworks }},
                "outboundTag": "blocked"
            }
        ]
    }
}
`)

// templateData is the configuration plus the TLS file paths, which are not
// in xray.toml because they are always the node's own.
type templateData struct {
	VLESS   interface{}
	Reality interface{}
	API     interface{}

	TLSCertPath string
	TLSKeyPath  string
	LogLevel    string

	// The egress policy: see common.Egress.
	BlockedNetworks []string
	BlockedDomain   string
	SMTPPort        int
	AllowSMTP       bool
}

// logLevel is xray's error-log level. Its warnings carry client addresses and
// the destinations clients reach, so they are kept only when the node runs at
// debug; otherwise only errors are logged. The access log stays off either way.
func logLevel() string {
	if common.Verbose() {
		return "warning"
	}

	return "error"
}
