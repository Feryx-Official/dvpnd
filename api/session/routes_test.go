// SPDX-License-Identifier: Apache-2.0

package session

import (
	"testing"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// TestLegacyEndpointOffByDefault: the replayable legacy endpoint exists only
// when the operator turns it on; the current handshake is always there.
//
// Rules: [HS-4].
func TestLegacyEndpointOffByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	routes := func(legacy bool) map[string]bool {
		config := types.NewConfig().WithDefaultValues()
		config.Node.LegacyHandshake = legacy
		ctx := context.NewContext().WithConfig(config).WithLogger(cmtlog.NewNopLogger())

		r := gin.New()
		RegisterRoutes(ctx, r)
		got := map[string]bool{}
		for _, route := range r.Routes() {
			got[route.Method+" "+route.Path] = true
		}
		return got
	}

	off := routes(false)
	if !off["POST /"] || off["POST /accounts/:acc_address/sessions/:id"] {
		t.Fatalf("default routes: %v", off)
	}
	if on := routes(true); !on["POST /"] || !on["POST /accounts/:acc_address/sessions/:id"] {
		t.Fatalf("routes with legacy_handshake: %v", on)
	}
	if types.NewConfig().WithDefaultValues().Node.LegacyHandshake {
		t.Fatal("legacy_handshake must default to false")
	}
}
