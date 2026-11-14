// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package utils

import (
	"fmt"
	"io"
	"text/tabwriter"

	base "github.com/Feryx-Official/feryxhub/v12/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
)

func WriteKeys(w io.Writer, keys ...*keyring.Record) error {
	tw := tabwriter.NewWriter(w, 1, 1, 1, ' ', 0)
	if _, err := fmt.Fprintf(
		tw, "%s\t%s\t%s\n",
		"Name", "Address", "Operator",
	); err != nil {
		return err
	}

	for i := 0; i < len(keys); i++ {
		accAddr, err := keys[i].GetAddress()
		if err != nil {
			return err
		}

		var (
			name     = keys[i].Name
			address  = base.NodeAddress(accAddr.Bytes()).String()
			operator = accAddr.String()
		)

		if _, err := fmt.Fprintf(
			tw, "%s\t%s\t%s\n",
			name, address, operator,
		); err != nil {
			return err
		}
	}

	return tw.Flush()
}
