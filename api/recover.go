// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// recoverPanics answers a request whose handler panicked with a 500 and logs
// the panic with its stack, so one bad request neither kills its connection
// unanswered nor goes unseen. gin's own Recovery is not used: it dumps the
// request, headers and all, to stderr.
func recoverPanics(ctx *context.Context) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		ctx.Log().Error("Request handler panicked",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"error", fmt.Sprint(err),
			"stack", string(debug.Stack()),
		)
		c.AbortWithStatusJSON(http.StatusInternalServerError,
			types.NewResponseError(types.CodeInternal, types.InternalErrorMessage))
	})
}
