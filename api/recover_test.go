// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// TestRecoverPanics: a handler that panics gets the client a 500 with the
// generic message and the operator a log line with the panic, and the
// request's headers go nowhere.
//
// Rules: [HS-9].
func TestRecoverPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	ctx := context.NewContext().WithLogger(cmtlog.NewTMLogger(&buf))

	r := gin.New()
	r.Use(logRefusals(ctx), recoverPanics(ctx))
	r.POST("/", func(*gin.Context) { panic("nil map in the handler") })

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Secret-Header", "do-not-log-me")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), types.InternalErrorMessage) {
		t.Fatalf("reply %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "nil map") {
		t.Fatalf("the panic reached the client: %s", rec.Body.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Request handler panicked") || !strings.Contains(out, "nil map in the handler") {
		t.Fatalf("panic not logged: %s", out)
	}
	if strings.Contains(out, "do-not-log-me") {
		t.Fatalf("request headers logged: %s", out)
	}
}
