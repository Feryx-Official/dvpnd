// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"net/http"

	"github.com/cosmos/cosmos-sdk/version"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/api/session"
	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// serverHeader names the node software on every response, the way any HTTP
// server does, so anyone looking at the node (an aggregator, a client
// developer, curl) can tell which implementation and version answered.
func serverHeader() gin.HandlerFunc {
	value := types.AppName + "/" + version.Version

	return func(c *gin.Context) {
		c.Header("Server", value)
		c.Next()
	}
}

// replySigningHeader says on every response that the node signs its
// handshake replies (session.ReplySigningHeader).
func replySigningHeader() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header(session.ReplySigningHeader, session.ReplySigningScheme)
		c.Next()
	}
}

// bodyCapture keeps a copy of a small response body so a refusal can be
// logged with the reason the client was given.
type bodyCapture struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *bodyCapture) Write(b []byte) (int, error) {
	if w.body.Len() < 512 {
		w.body.Write(b[:min(len(b), 512-w.body.Len())])
	}

	return w.ResponseWriter.Write(b)
}

// logRefusals logs every request the node answers with an error status: what
// was asked and the reason given. A node operator otherwise has no way to see
// why a client or an aggregator's probe was turned away.
//
// The client's IP address goes only into the debug line every request gets.
// It is the one thing here the public chain does not already show next to the
// session, so a node at the default log level keeps no client addresses. It
// is the address the connection came from (RemoteIP), never a forwarding
// header the client could write itself.
func logRefusals(ctx *context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		w := &bodyCapture{ResponseWriter: c.Writer}
		c.Writer = w

		c.Next()

		status := c.Writer.Status()
		ctx.Log().Debug("Request answered",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"client", c.RemoteIP(),
			"agent", c.Request.UserAgent(),
		)
		// A rate-limited attempt is left at debug: a flood would otherwise
		// fill the log (the limiter logs once per window that it is refusing).
		if status < http.StatusBadRequest || status == http.StatusTooManyRequests {
			return
		}

		ctx.Log().Error("Request refused",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"agent", c.Request.UserAgent(),
			"reply", w.body.String(),
		)
	}
}
