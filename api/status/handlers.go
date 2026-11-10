// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package status

import (
	"net/http"

	"github.com/cosmos/cosmos-sdk/version"
	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// HandlerGetRoot serves GET /, the document current client apps and node
// aggregators read to learn what a node runs and how it is doing.
func HandlerGetRoot(ctx *context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, types.NewResponseResult(&ResponseGetRoot{
			Addr:         ctx.Address().String(),
			Downlink:     ctx.Bandwidth().Download.String(),
			HandshakeDNS: ctx.Config().Handshake.Enable,
			Location: &RootLocation{
				City:        ctx.Location().City,
				Country:     ctx.Location().Country,
				CountryCode: ctx.Location().CountryCode,
				Latitude:    ctx.Location().Latitude,
				Longitude:   ctx.Location().Longitude,
			},
			Moniker:         ctx.Moniker(),
			Peers:           ctx.Service().PeerCount(),
			ServiceType:     ctx.Service().Name(),
			ServiceMetadata: ctx.Service().PublicMetadata(),
			Uplink:          ctx.Bandwidth().Upload.String(),
			Version:         &RootVersion{Name: types.AppName, Tag: version.Version, Commit: version.Commit},
		}))
	}
}

func HandlerGetStatus(ctx *context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, types.NewResponseResult(statusResponse(ctx)))
	}
}

func statusResponse(ctx *context.Context) *ResponseGetStatus {
	return &ResponseGetStatus{
		Address: ctx.Address().String(),
		Bandwidth: &Bandwidth{
			Upload:   ctx.Bandwidth().Upload.Int64(),
			Download: ctx.Bandwidth().Download.Int64(),
			Source:   ctx.BandwidthSource(),
		},
		Handshake: &Handshake{
			Enable: ctx.Config().Handshake.Enable,
			Peers:  ctx.Config().Handshake.Peers,
		},
		IntervalSetSessions:    ctx.IntervalSetSessions(),
		IntervalUpdateSessions: ctx.IntervalUpdateSessions(),
		IntervalUpdateStatus:   ctx.IntervalUpdateStatus(),
		Location: &Location{
			City:        ctx.Location().City,
			Country:     ctx.Location().Country,
			CountryCode: ctx.Location().CountryCode,
			Latitude:    ctx.Location().Latitude,
			Longitude:   ctx.Location().Longitude,
			Source:      ctx.Location().Source,
		},
		Moniker:        ctx.Moniker(),
		Operator:       ctx.Operator().String(),
		Peers:          ctx.Service().PeerCount(),
		GigabytePrices: ctx.GigabytePrices().String(),
		HourlyPrices:   ctx.HourlyPrices().String(),
		QOS: &QOS{
			MaxPeers: ctx.Config().QOS.MaxPeers,
		},
		Type:    ctx.Service().Type(),
		Version: version.Version,
	}
}
