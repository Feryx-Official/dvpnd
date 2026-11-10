// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package api

import (
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/api/session"
	"github.com/Feryx-Official/dvpnd/v9/api/status"
	"github.com/Feryx-Official/dvpnd/v9/context"
)

func RegisterRoutes(ctx *context.Context, r gin.IRouter) {
	r.Use(allowBrowsers())
	r.Use(serverHeader())
	r.Use(replySigningHeader())
	r.Use(logRefusals(ctx))
	r.Use(recoverPanics(ctx))
	r.Use(limitBody())
	session.RegisterRoutes(ctx, r)
	status.RegisterRoutes(ctx, r)
}
