// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cosmos/cosmos-sdk/version"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
)

// Rules: [HS-12], [PV-1].
func TestLogRefusals(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	ctx := context.NewContext().WithLogger(cmtlog.NewTMLogger(&buf))

	r := gin.New()
	r.Use(logRefusals(ctx))
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"success": true}) })
	r.POST("/", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": gin.H{"code": 2, "message": "signature does not verify"}})
	})

	serve := func(method, path string) {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("User-Agent", "probe/1.0")
		req.Header.Set("X-Forwarded-For", "198.51.100.7")
		req.RemoteAddr = "203.0.113.9:4242"
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	serve(http.MethodGet, "/ok")
	if out := buf.String(); !strings.HasPrefix(out, "D[") || !strings.Contains(out, "Request answered") || strings.Contains(out, "refused") {
		t.Fatalf("a successful request is logged at debug level only: %s", out)
	}
	buf.Reset()

	serve(http.MethodPost, "/")
	var debugLine, errorLine string
	for _, line := range strings.Split(buf.String(), "\n") {
		switch {
		case strings.HasPrefix(line, "D[") && strings.Contains(line, "Request answered"):
			debugLine = line
		case strings.HasPrefix(line, "E[") && strings.Contains(line, "Request refused"):
			errorLine = line
		}
	}
	for _, want := range []string{"method=POST", "path=/", "status=400", "agent=probe/1.0", "signature does not verify"} {
		if !strings.Contains(errorLine, want) {
			t.Errorf("refusal line lacks %q: %s", want, errorLine)
		}
	}
	if strings.Contains(errorLine, "203.0.113.9") || strings.Contains(errorLine, "198.51.100.7") {
		t.Errorf("the refusal line must not carry the client's address: %s", errorLine)
	}
	if !strings.Contains(debugLine, "client=203.0.113.9") {
		t.Errorf("the debug line must carry the connection's address: %s", debugLine)
	}
	if strings.Contains(debugLine, "198.51.100.7") {
		t.Errorf("a forwarding header must not stand in for the client's address: %s", debugLine)
	}

	buf.Reset()
	serve(http.MethodGet, "/nowhere")
	if !strings.Contains(buf.String(), "status=404") || !strings.Contains(buf.String(), "path=/nowhere") {
		t.Fatalf("unknown path must be logged: %s", buf.String())
	}
}

// Rules: [CT-2].
func TestServerHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	version.Version = "1.2.3"

	r := gin.New()
	r.Use(serverHeader())
	r.GET("/", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"success": true}) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Server"); got != "dvpnd/1.2.3" {
		t.Fatalf("Server header: got %q, want dvpnd/1.2.3", got)
	}
}

// TestReplySigningHeader: every response, a refusal and an unknown path
// included, says that the node signs its handshake replies.
//
// Rules: [HS-7].
func TestReplySigningHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(replySigningHeader())
	r.GET("/", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"success": true}) })
	r.POST("/", func(c *gin.Context) { c.JSON(http.StatusBadRequest, gin.H{"success": false}) })

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/", nil),
		httptest.NewRequest(http.MethodPost, "/", nil),
		httptest.NewRequest(http.MethodGet, "/nowhere", nil),
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Header().Get("X-Dvpnd-Reply-Signing"); got != "dvpnd/handshake-reply/v1" {
			t.Errorf("%s %s: X-Dvpnd-Reply-Signing %q", req.Method, req.URL.Path, got)
		}
	}
}
