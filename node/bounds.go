// SPDX-License-Identifier: Apache-2.0

package node

import (
	"math"
	"time"

	sdkmath "cosmossdk.io/math"
	base "github.com/Feryx-Official/feryxhub/v12/types"
	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	sessiontypes "github.com/Feryx-Official/feryxhub/v12/x/session/types/v3"
	subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v3"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// boundReport is the report the node sends for a stored session: its totals,
// cut to what the chain records for the session, and send says whether it
// carries anything the session is paid on that the chain does not hold yet.
//
// The chain refuses a report that claims more than the session may record,
// and with it every other report in the transaction. What the session's peer
// moved beyond that is service nobody pays for, so it is left out of the
// report rather than claimed:
//
//   - the total bytes stop at the session's byte ceiling, which the session
//     query gives: a direct session's purchased bytes, and for a plan session
//     the allocation's headroom on the current ledger, G - (U - S), where
//     zero means none is left; and at the chain's counter limit;
//   - an hourly direct session's duration stops at its paid hours and at the
//     service time the chain observed, from its start to the latest block, or
//     to its cancellation once it is pending: the final-report grace is not
//     billable time.
//
// A cut report keeps what the chain holds, so it is never refused as lower
// than the last one. send is false when the report adds no bytes and, for an
// hourly session, no time: a session that has reached its ceiling, or idles,
// gets no report, and the chain's idle timeout ends it.
//
// chainTime is asked only for an active hourly session. It is the time of the
// latest block; the block a report lands in is never earlier, so the bound
// taken at it still holds there.
func boundReport(item types.Session, session sessiontypes.Session, chainTime func() (time.Time, error)) (r types.Report, send bool, err error) {
	r = types.Report{ID: item.ID, Download: item.Download, Upload: item.Upload, Duration: item.Duration()}

	var (
		heldDownload = session.GetDownloadBytes()
		heldUpload   = session.GetUploadBytes()
		ceiling      = base.MaxCounterBytes
		hourly       *nodetypes.Session
	)
	switch s := session.(type) {
	case *nodetypes.Session:
		if s.MaxBytes.IsPositive() {
			ceiling = sdkmath.MinInt(ceiling, s.MaxBytes)
		}
		if s.MaxDuration > 0 {
			hourly = s
		}
	case *subscriptiontypes.Session:
		headroom := s.GetMaxBytes()
		if headroom.IsNil() {
			headroom = sdkmath.ZeroInt()
		}
		ceiling = sdkmath.MinInt(ceiling, headroom)
	}

	download, upload := sdkmath.NewInt(r.Download), sdkmath.NewInt(r.Upload)
	if download.Add(upload).GT(ceiling) {
		download, upload = splitHeadroom(heldDownload, heldUpload, download, upload, ceiling)
		r.Download, r.Upload = int64Of(download), int64Of(upload)
	}

	if hourly != nil {
		// The end of the service the chain observed (BillableEnd): a pending
		// session is billed to its cancellation, an active one to its block.
		end := hourly.StatusAt
		if !hourly.Status.Equal(v1base.StatusInactivePending) {
			if end, err = chainTime(); err != nil {
				return r, false, err
			}
		}

		limit := hourly.MaxDuration
		observed := end.Sub(hourly.StartAt)
		if observed < limit {
			limit = max(observed, 0)
		}
		if r.Duration > limit {
			r.Duration = max(limit, hourly.Duration)
		}
	}

	send = !download.Equal(heldDownload) || !upload.Equal(heldUpload) ||
		(hourly != nil && r.Duration != hourly.Duration)

	return r, send, nil
}

// splitHeadroom cuts a report whose total passes the ceiling down to it. What
// the chain holds stays; the headroom left above it goes to the two
// directions in proportion to what each moved since, the download's share
// rounded down. Neither direction gets more than it moved.
func splitHeadroom(heldDownload, heldUpload, download, upload, ceiling sdkmath.Int) (sdkmath.Int, sdkmath.Int) {
	var (
		movedDownload = sdkmath.MaxInt(download.Sub(heldDownload), sdkmath.ZeroInt())
		movedUpload   = sdkmath.MaxInt(upload.Sub(heldUpload), sdkmath.ZeroInt())
		moved         = movedDownload.Add(movedUpload)
		room          = ceiling.Sub(heldDownload).Sub(heldUpload)
	)
	if !room.IsPositive() || moved.IsZero() {
		return heldDownload, heldUpload
	}

	share := room.Mul(movedDownload).Quo(moved)

	return heldDownload.Add(share), heldUpload.Add(room.Sub(share))
}

// int64Of is v as an int64 counter, saturating at the largest one.
func int64Of(v sdkmath.Int) int64 {
	switch {
	case v.IsInt64():
		return v.Int64()
	case v.IsNegative():
		return 0
	default:
		return math.MaxInt64
	}
}
