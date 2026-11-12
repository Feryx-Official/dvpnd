// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/api/session"
)

// allowBrowsers lets client apps that run in a browser call the node: any
// origin, GET and POST with a JSON body. The allowed header is the header's
// name, Content-Type: a browser's preflight asks for it by name. Such a
// client must be able to read the reply's signature, and that the node
// signs, so both headers are exposed.
func allowBrowsers() gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowAllOrigins: true,
		AllowMethods:    []string{http.MethodGet, http.MethodPost},
		AllowHeaders:    []string{"Content-Type"},
		ExposeHeaders:   []string{session.ReplySignatureHeader, session.ReplySigningHeader},
	})
}
