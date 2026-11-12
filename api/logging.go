// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"net/http"
	"strings"

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

// maxLoggedField bounds the path and the User-Agent in a log line: both are
// written by the client, who could otherwise make each line kilobytes long.
const maxLoggedField = 200

// clip cuts s to maxLoggedField bytes, dropping a character the cut splits,
// and marks the cut.
func clip(s string) string {
	if len(s) <= maxLoggedField {
		return s
	}

	return strings.ToValidUTF8(s[:maxLoggedField], "") + "..."
}

// logRefusals logs every request the node answers with an error status: what
// was asked and the reason given. A node operator otherwise has no way to see
// why a client or an aggregator's probe was turned away.
//
// A request for a path the node does not serve stays at debug: scanners send
// them by the thousand, and at error level they would bury the refusals that
// matter. A refusal on a route the node serves, a 404 included, is logged at
// error. The path and the User-Agent are clipped in every line.
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
		path, agent := clip(c.Request.URL.Path), clip(c.Request.UserAgent())
		ctx.Log().Debug("Request answered",
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"client", c.RemoteIP(),
			"agent", agent,
		)
		// A rate-limited attempt is left at debug: a flood would otherwise
		// fill the log (the limiter logs once per window that it is refusing).
		// So is a path no route matched (FullPath is empty).
		if status < http.StatusBadRequest || status == http.StatusTooManyRequests || c.FullPath() == "" {
			return
		}

		ctx.Log().Error("Request refused",
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"agent", agent,
			"reply", w.body.String(),
		)
	}
}
