// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package status

import (
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
)

func RegisterRoutes(ctx *context.Context, r gin.IRouter) {
	r.GET("/", HandlerGetRoot(ctx))
	r.GET("/status", HandlerGetStatus(ctx))
}
