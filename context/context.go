// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package context

import (
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	base "github.com/sentinel-official/sentinelhub/v12/types"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	"gorm.io/gorm"

	geoiptypes "github.com/Feryx-Official/dvpnd/v9/libs/geoip/types"
	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

type Context struct {
	// admission serialises every change to the peer set together with the
	// session table: a handshake's checks, AddPeer and its row, and the jobs'
	// removals. Without it concurrent handshakes all pass the max_peers and
	// duplicate checks, and a job can evict a peer whose row is not written yet.
	admission sync.Mutex

	bandwidth       *v1base.Bandwidth
	bandwidthSource string
	client          *lite.Client
	config          *types.Config
	database        *gorm.DB
	handler         http.Handler
	location        *geoiptypes.GeoIPLocation
	logger          cmtlog.Logger
	service         types.Service
}

func NewContext() *Context {
	return &Context{}
}

func (c *Context) WithBandwidth(v *v1base.Bandwidth) *Context        { c.bandwidth = v; return c }
func (c *Context) WithBandwidthSource(v string) *Context             { c.bandwidthSource = v; return c }
func (c *Context) WithClient(v *lite.Client) *Context                { c.client = v; return c }
func (c *Context) WithConfig(v *types.Config) *Context               { c.config = v; return c }
func (c *Context) WithDatabase(v *gorm.DB) *Context                  { c.database = v; return c }
func (c *Context) WithHandler(v http.Handler) *Context               { c.handler = v; return c }
func (c *Context) WithLocation(v *geoiptypes.GeoIPLocation) *Context { c.location = v; return c }
func (c *Context) WithLogger(v cmtlog.Logger) *Context               { c.logger = v; return c }
func (c *Context) WithService(v types.Service) *Context              { c.service = v; return c }

// Admission is the lock described on the admission field.
func (c *Context) Admission() *sync.Mutex { return &c.admission }

func (c *Context) Address() base.NodeAddress           { return c.Operator().Bytes() }
func (c *Context) Bandwidth() *v1base.Bandwidth        { return c.bandwidth }
func (c *Context) BandwidthSource() string             { return c.bandwidthSource }
func (c *Context) Client() *lite.Client                { return c.client }
func (c *Context) Config() *types.Config               { return c.config }
func (c *Context) Database() *gorm.DB                  { return c.database }
func (c *Context) Handler() http.Handler               { return c.handler }
func (c *Context) IntervalSetSessions() time.Duration  { return c.Config().Node.IntervalSetSessions }
func (c *Context) IntervalUpdateStatus() time.Duration { return c.Config().Node.IntervalUpdateStatus }
func (c *Context) ListenOn() string                    { return c.Config().Node.ListenOn }
func (c *Context) Location() *geoiptypes.GeoIPLocation { return c.location }
func (c *Context) Log() cmtlog.Logger                  { return c.logger }
func (c *Context) Moniker() string                     { return c.Config().Node.Moniker }

// Operator is the node account: the granter when the node signs with a hot
// key, the signing key's own account otherwise.
func (c *Context) Operator() sdk.AccAddress {
	if granter := c.client.Granter(); granter != nil {
		return granter
	}

	return c.client.FromAddress()
}

func (c *Context) RemoteURL() string      { return c.Config().Node.RemoteURL }
func (c *Context) Service() types.Service { return c.service }

func (c *Context) IntervalUpdateSessions() time.Duration {
	return c.Config().Node.IntervalUpdateSessions
}

func (c *Context) IPv4Address() net.IP {
	addr := c.Config().Node.IPv4Address
	if addr == "" {
		addr = c.Location().IP
	}

	return net.ParseIP(addr).To4()
}

func (c *Context) GigabytePrices() v1base.Prices {
	prices, err := types.ParsePrices(c.Config().Node.GigabytePrices)
	if err != nil {
		panic(err)
	}

	return prices
}

func (c *Context) HourlyPrices() v1base.Prices {
	prices, err := types.ParsePrices(c.Config().Node.HourlyPrices)
	if err != nil {
		panic(err)
	}

	return prices
}

// RemoteAddrs returns the host:port form of remote_url, which is what the chain
// stores in a node's remote_addrs (v3). Clients prepend the scheme themselves.
func (c *Context) RemoteAddrs() []string {
	u, err := url.Parse(c.RemoteURL())
	if err != nil {
		panic(err)
	}

	return []string{u.Host}
}
