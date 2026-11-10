// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// requireTLS refuses a handshake that arrives over plain HTTP. The node's port
// answers both (the status pages stay readable either way), but a handshake
// carries the client's peer request, which for the proxy protocols is the
// UUID the session logs in with. The node only advertises an https remote_url,
// so a client that follows it is never refused.
func requireTLS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.TLS == nil {
			c.AbortWithStatusJSON(http.StatusForbidden,
				types.NewResponseError(11, errors.New("the handshake must be made over https")))
			return
		}
		c.Next()
	}
}

// Each handshake attempt costs the node chain queries, and anyone can sign one
// with a throwaway key. Without a cap a single host could make the node flood
// its RPC providers until they throttle it and real clients are turned away.
// A client needs one handshake per session plus a few retries, so the cap
// leaves room for many users behind one carrier-grade NAT.
const (
	handshakeLimit  = 30
	handshakeWindow = time.Minute
)

// rateLimiter counts attempts per client block in fixed windows. The counts
// live in memory only and are dropped when a window ends, so they never
// become a record of who connected.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	start  time.Time
	counts map[string]int
	warned bool // whether this window's first refusal has been logged
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, now: time.Now, counts: map[string]int{}}
}

// allow records an attempt from key and reports whether it is within the
// limit. For a refusal it also returns how long until the window ends, and
// whether it is the window's first refusal.
func (l *rateLimiter) allow(key string) (ok bool, wait time.Duration, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if now.Sub(l.start) >= l.window {
		l.start = now
		l.counts = map[string]int{}
		l.warned = false
	}
	if l.counts[key] >= l.limit {
		first = !l.warned
		l.warned = true

		return false, l.start.Add(l.window).Sub(now), first
	}
	l.counts[key]++

	return true, 0, false
}

// clientBlock is what the limit counts: the IPv4 address, or the /64 an IPv6
// address belongs to, since one subscriber usually holds a whole /64 and could
// otherwise step through it.
func clientBlock(remoteIP string) string {
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return remoteIP
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}

	return ip.Mask(net.CIDRMask(64, 128)).String()
}

// limitHandshakes answers 429 once a client block has used its attempts for
// the current window. The refusals themselves are logged at debug only (a
// flood would otherwise fill the log); one line per window tells the operator
// that limiting has started.
func limitHandshakes(ctx *context.Context, l *rateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ok, wait, first := l.allow(clientBlock(c.RemoteIP())); !ok {
			if first {
				ctx.Log().Info("Refusing handshake attempts over the limit until the window ends",
					"limit", l.limit, "window", l.window)
			}
			c.Header("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			c.AbortWithStatusJSON(http.StatusTooManyRequests,
				types.NewResponseError(12, errors.New("too many handshake attempts; retry later")))
			return
		}
		c.Next()
	}
}
