// SPDX-License-Identifier: Apache-2.0

package types

import "strings"

// OneLine is how a value a client wrote (a request path, a User-Agent, an
// error that quotes the request) goes into a log entry: its line breaks
// written as the two characters \r and \n, so it cannot start a line of its
// own. The node's JSON and console writers escape them too; this holds
// whatever logger the node is given.
func OneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", `\r`)

	return strings.ReplaceAll(s, "\n", `\n`)
}
