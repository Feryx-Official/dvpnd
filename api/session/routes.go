// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package session

import (
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
)

func RegisterRoutes(ctx *context.Context, router gin.IRouter) {
	// Both handshake routes share one attempt limit, checked after the TLS
	// requirement so a refused plain-HTTP request does not use it up.
	limit := limitHandshakes(ctx, newRateLimiter(handshakeLimit, handshakeWindow))

	// Current client apps handshake at the root path.
	router.POST("/", requireTLS(), limit, HandlerHandshake(ctx))
	// The legacy endpoint only when the operator turns it on: its signature
	// covers the session id alone, so a captured request can be replayed
	// with another peer key, which evicts the real client's peer.
	if ctx.Config().Node.LegacyHandshake {
		router.POST("/accounts/:acc_address/sessions/:id", requireTLS(), limit, HandlerAddSession(ctx))
	}
}
