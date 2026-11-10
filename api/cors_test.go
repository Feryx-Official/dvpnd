// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/api/session"
	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// TestBrowsersCanReadTheSignature: a client app running in a browser can
// call the node, and can read the reply's signature and that the node signs;
// without the exposure the browser hides both headers from it.
//
// Rules: [HS-7].
func TestBrowsersCanReadTheSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.NewContext().WithConfig(types.NewConfig().WithDefaultValues()).WithLogger(cmtlog.NewNopLogger())
	r := gin.New()
	RegisterRoutes(ctx, r)

	req := httptest.NewRequest(http.MethodGet, "/no-such-path", nil)
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	exposed := strings.ToLower(rec.Header().Get("Access-Control-Expose-Headers"))
	for _, h := range []string{session.ReplySignatureHeader, session.ReplySigningHeader} {
		if !strings.Contains(exposed, strings.ToLower(h)) {
			t.Errorf("%s is not exposed to browsers (Access-Control-Expose-Headers: %q)", h, exposed)
		}
	}

	pre := httptest.NewRequest(http.MethodOptions, "/", nil)
	pre.Header.Set("Origin", "https://app.example")
	pre.Header.Set("Access-Control-Request-Method", http.MethodPost)
	pre.Header.Set("Access-Control-Request-Headers", types.ContentType)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, pre)
	if rec.Code >= 300 || !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), http.MethodPost) {
		t.Fatalf("a browser's preflight for the handshake: %d, allow-methods %q", rec.Code, rec.Header().Get("Access-Control-Allow-Methods"))
	}
}
