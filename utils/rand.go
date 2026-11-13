// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package utils

import (
	"crypto/rand"
	"math/big"
)

func RandomPort() uint16 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<16-1<<10))
	if err != nil {
		panic(err)
	}

	return uint16(n.Int64() + 1<<10) //nolint:gosec // G115: n < 1<<16-1<<10, so n+1<<10 < 1<<16
}
