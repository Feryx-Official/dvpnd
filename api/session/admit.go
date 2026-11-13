// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/gin-gonic/gin"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	sessiontypes "github.com/sentinel-official/sentinelhub/v12/x/session/types/v3"
	v2subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v2"
	subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v3"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// apiError carries the HTTP status and the numeric code the response envelope
// reports. Codes are the ones upstream used, so client error handling that keys
// on them keeps working.
type apiError struct {
	Status int
	Code   int
	Err    error
}

func (e *apiError) Error() string { return e.Err.Error() }

func newAPIError(status, code int, err error) *apiError {
	return &apiError{Status: status, Code: code, Err: err}
}

// replyError answers a handshake with an error. When the node itself failed
// (a 5xx) the detail is logged and the client gets a generic message: an RPC
// endpoint's error or the proxy's is the operator's business.
func replyError(ctx *context.Context, c *gin.Context, status, code int, err error) {
	if status >= http.StatusInternalServerError {
		ctx.Log().Error("Handshake failed inside the node",
			"path", types.OneLine(c.Request.URL.Path), "code", code, "error", types.OneLine(err.Error()))
		err = errors.New(types.InternalErrorMessage)
	}
	c.JSON(status, types.NewResponseError(code, err))
}

// admitRequest is a session admission that has already been authenticated:
// AccAddress is the account proven to hold the key, ID the on-chain session,
// PeerData the service-specific peer material (WireGuard public key, or proxy
// byte + UUID for V2Ray) and Request the peer request it was parsed from,
// nil from the legacy endpoint.
type admitRequest struct {
	AccAddress sdk.AccAddress
	ID         uint64
	PeerData   []byte
	Request    []byte
}

// PeerKey is the identity the service and the local database use for the peer.
func (r admitRequest) PeerKey() string {
	return base64.StdEncoding.EncodeToString(r.PeerData)
}

type admitResult struct {
	Session sessiontypes.Session
	Peer    []byte // what Service.AddPeer returned (WireGuard: assigned v4+v6)
}

// chainQuerier is the part of the chain client admission uses; the node
// passes its *lite.Client and tests a fake.
type chainQuerier interface {
	QuerySession(id uint64) (sessiontypes.Session, error)
	QuerySubscription(id uint64) (*subscriptiontypes.Subscription, error)
	QueryAllocation(id uint64, accAddr sdk.AccAddress) (*v2subscriptiontypes.Allocation, error)
}

// admission is what the chain says about a session, gathered before the
// admission lock is taken.
type admission struct {
	session        sessiontypes.Session
	subscriptionID uint64
	// remainingBytes is the cap from the session's own max_bytes; 0 is none.
	remainingBytes int64
	// alloc is the account's allocation on a plan subscription, nil
	// otherwise; its utilised bytes do not yet count what this node served.
	alloc *v2subscriptiontypes.Allocation
}

// admit checks the session on the chain, enforces the byte caps and the paid
// hours, evicts any earlier peer of the same account, adds the peer to the VPN
// service and records the session locally. It is shared by the legacy and the
// current handshake endpoints.
//
// The chain queries run first, without a lock, since they take a network
// round trip each. Everything that reads or changes the peer set and the
// session table then runs under the node's admission lock, which the jobs
// share, so concurrent handshakes cannot together exceed max_peers or admit
// one session or key twice.
func admit(ctx *context.Context, chain chainQuerier, req admitRequest) (*admitResult, *apiError) {
	// Cheap refusals before any chain query; checked again under the lock.
	if apiErr := checkCapacity(ctx, req); apiErr != nil {
		return nil, apiErr
	}

	a, apiErr := queryAdmission(ctx, chain, req)
	if apiErr != nil {
		return nil, apiErr
	}

	lock := ctx.Admission()
	lock.Lock()
	defer lock.Unlock()

	if apiErr = checkCapacity(ctx, req); apiErr != nil {
		return nil, apiErr
	}

	// One peer per account, whatever its sessions: the account's earlier
	// peers, of node and plan sessions alike, are evicted below. What they
	// moved since the last pass is stored first; it also counts against the
	// allocation. A struct condition would skip a node session's zero
	// subscription.
	var earlier []types.Session
	err := ctx.Database().Model(&types.Session{}).Where("address = ?", req.AccAddress.String()).
		Find(&earlier).Error
	if err != nil {
		return nil, newAPIError(http.StatusInternalServerError, 9, err)
	}
	if err = keepUsage(ctx, earlier); err != nil {
		return nil, newAPIError(http.StatusInternalServerError, 9, err)
	}

	remainingBytes := a.remainingBytes
	if a.alloc != nil {
		// Count bytes this node has already served on the same allocation
		// but not yet reported to the chain.
		var items []types.Session
		err = ctx.Database().Model(&types.Session{}).Where(&types.Session{
			Subscription: a.subscriptionID,
			Address:      req.AccAddress.String(),
		}).Find(&items).Error
		if err != nil {
			return nil, newAPIError(http.StatusInternalServerError, 8, err)
		}

		utilised := a.alloc.UtilisedBytes
		for i := 0; i < len(items); i++ {
			utilised = utilised.Add(sdkmath.NewInt(items[i].ServedBytes()))
		}
		if utilised.GTE(a.alloc.GrantedBytes) {
			return nil, newAPIError(http.StatusBadRequest, 8,
				fmt.Errorf("invalid allocation; granted bytes %s, utilised bytes %s", a.alloc.GrantedBytes, utilised))
		}

		if left := clampInt64(a.alloc.GrantedBytes.Sub(utilised)); remainingBytes == 0 || left < remainingBytes {
			remainingBytes = left
		}
	}

	// The account's earlier peers go before its new one comes.
	for i := 0; i < len(earlier); i++ {
		if err = ctx.RemovePeerIfExists(earlier[i].Key); err != nil {
			return nil, newAPIError(http.StatusInternalServerError, 9, err)
		}
	}

	peer, err := types.AddPeer(ctx.Service(), req.Request, req.PeerData)
	if err != nil {
		return nil, newAPIError(http.StatusInternalServerError, 10, err)
	}

	// Start the reported totals from what the chain already holds: a client
	// that reconnects after a node restart keeps its session, and the chain
	// refuses any report lower than the last one (types.Session explains).
	var (
		baseDownload = clampInt64(a.session.GetDownloadBytes())
		baseUpload   = clampInt64(a.session.GetUploadBytes())
	)

	err = ctx.Database().Model(&types.Session{}).Create(&types.Session{
		ID:           req.ID,
		Subscription: a.subscriptionID,
		Key:          req.PeerKey(),
		Address:      req.AccAddress.String(),
		Available:    remainingBytes,
		Download:     baseDownload,
		Upload:       baseUpload,
		BaseDownload: baseDownload,
		BaseUpload:   baseUpload,
		BaseDuration: int64(a.session.GetDuration()),
		MaxDuration:  int64(a.session.GetMaxDuration()),
	}).Error
	if err != nil {
		// A peer without its row would be served unmetered until the next
		// set_sessions pass found it; take it out again.
		if rmErr := ctx.Service().RemovePeer(req.PeerData); rmErr != nil {
			ctx.Log().Error("could not remove a peer whose session was not recorded",
				"key", types.KeyTag(req.PeerKey()), "error", rmErr)
		}

		return nil, newAPIError(http.StatusInternalServerError, 10, err)
	}
	ctx.Log().Info("Added a new peer", "key", types.KeyTag(req.PeerKey()), "count", ctx.Service().PeerCount())

	return &admitResult{Session: a.session, Peer: peer}, nil
}

// keepUsage stores what the connected ones among items moved since the last
// pass. It runs under the admission lock, as the usage pass does.
func keepUsage(ctx *context.Context, items []types.Session) error {
	connected := false
	for i := 0; i < len(items) && !connected; i++ {
		ok, err := ctx.HasPeer(items[i].Key)
		if err != nil {
			return err
		}
		connected = ok
	}
	if !connected {
		return nil
	}

	peers, err := ctx.Service().Peers()
	if err != nil {
		return err
	}
	byKey := make(map[string]types.Peer, len(peers))
	for _, p := range peers {
		byKey[p.Key] = p
	}
	for _, item := range items {
		if p, ok := byKey[item.Key]; ok {
			if _, err = ctx.StoreUsage(item, p); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkCapacity refuses a handshake when the node is full or already holds
// the session or the key.
func checkCapacity(ctx *context.Context, req admitRequest) *apiError {
	if ctx.Service().PeerCount() >= ctx.Config().QOS.MaxPeers {
		return newAPIError(http.StatusBadRequest, 1,
			fmt.Errorf("reached maximum peers limit %d", ctx.Config().QOS.MaxPeers))
	}

	exists, err := sessionExists(ctx, &types.Session{ID: req.ID})
	if err != nil {
		return newAPIError(http.StatusInternalServerError, 3, err)
	}
	if exists {
		return newAPIError(http.StatusConflict, 3,
			fmt.Errorf("session %d already exists in database", req.ID))
	}

	exists, err = sessionExists(ctx, &types.Session{Key: req.PeerKey()})
	if err != nil {
		return newAPIError(http.StatusInternalServerError, 3, err)
	}
	if exists {
		return newAPIError(http.StatusConflict, 3,
			fmt.Errorf("key %s for service already exists", types.KeyTag(req.PeerKey())))
	}

	return nil
}

func sessionExists(ctx *context.Context, where *types.Session) (bool, error) {
	var n int64
	err := ctx.Database().Model(&types.Session{}).Where(where).Count(&n).Error

	return n > 0, err
}

// queryAdmission asks the chain whether the session may be served here, and
// under which byte caps; an hourly session must have time left.
func queryAdmission(ctx *context.Context, chain chainQuerier, req admitRequest) (*admission, *apiError) {
	session, err := chain.QuerySession(req.ID)
	if err != nil {
		return nil, newAPIError(http.StatusInternalServerError, 5, err)
	}
	if session == nil {
		return nil, newAPIError(http.StatusNotFound, 5, fmt.Errorf("session %d does not exist", req.ID))
	}
	if !session.GetStatus().Equal(v1base.StatusActive) {
		return nil, newAPIError(http.StatusNotFound, 5,
			fmt.Errorf("invalid status %s for session %d", session.GetStatus(), session.GetID()))
	}
	if session.GetAccAddress() != req.AccAddress.String() {
		return nil, newAPIError(http.StatusBadRequest, 5,
			fmt.Errorf("account address mismatch; expected %s, got %s", req.AccAddress, session.GetAccAddress()))
	}
	if session.GetNodeAddress() != ctx.Address().String() {
		return nil, newAPIError(http.StatusBadRequest, 7,
			fmt.Errorf("node address mismatch; expected %s, got %s", ctx.Address(), session.GetNodeAddress()))
	}

	a := &admission{session: session}

	// Every session type may carry max_bytes on the chain; plan subscription
	// sessions are additionally bounded by the account's allocation.
	if max := session.GetMaxBytes(); max.IsPositive() {
		diff := max.Sub(session.TotalBytes())
		if !diff.IsPositive() {
			return nil, newAPIError(http.StatusBadRequest, 8,
				fmt.Errorf("session %d has used its %s bytes", session.GetID(), max))
		}
		a.remainingBytes = clampInt64(diff)
	}
	// An hourly session is paid for the duration the node reports, up to the
	// hours it paid for: time served beyond them is never paid.
	if max := session.GetMaxDuration(); max > 0 && session.GetDuration() >= max {
		return nil, newAPIError(http.StatusBadRequest, 8,
			fmt.Errorf("session %d has used its %s", session.GetID(), max))
	}

	s, ok := session.(*subscriptiontypes.Session)
	if !ok {
		return a, nil
	}
	a.subscriptionID = s.SubscriptionID

	subscription, err := chain.QuerySubscription(s.SubscriptionID)
	if err != nil {
		return nil, newAPIError(http.StatusInternalServerError, 6, err)
	}
	if subscription == nil {
		return nil, newAPIError(http.StatusNotFound, 6,
			fmt.Errorf("subscription %d does not exist", s.SubscriptionID))
	}
	if !subscription.Status.Equal(v1base.StatusActive) {
		return nil, newAPIError(http.StatusBadRequest, 6,
			fmt.Errorf("invalid status %s for subscription %d", subscription.Status, subscription.ID))
	}

	a.alloc, err = chain.QueryAllocation(subscription.ID, req.AccAddress)
	if err != nil {
		return nil, newAPIError(http.StatusInternalServerError, 8, err)
	}
	if a.alloc == nil {
		return nil, newAPIError(http.StatusNotFound, 8,
			fmt.Errorf("allocation %d/%s does not exist", subscription.ID, req.AccAddress))
	}

	return a, nil
}

// clampInt64 converts a chain integer to int64, saturating at MaxInt64 and
// treating a negative value as zero.
func clampInt64(v sdkmath.Int) int64 {
	if v.IsNegative() {
		return 0
	}
	if v.IsInt64() {
		return v.Int64()
	}

	return math.MaxInt64
}
