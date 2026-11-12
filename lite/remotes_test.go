// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cosmos/cosmos-sdk/client"
)

// TestEveryRemoteIsTried: a remote that fails is logged and the next one is
// asked; once one answers the rest are left alone; when all fail, the error
// names every remote with its own error. Queries, broadcasts and gas
// estimates all go through this loop.
//
// Rules: [CH-8].
func TestEveryRemoteIsTried(t *testing.T) {
	var log bytes.Buffer
	remotes := []string{"https://a.example:443", "https://b.example:443", "https://c.example:443"}
	c := NewDefaultClient().WithLogger(cmtlog.NewTMLogger(&log)).WithRemotes(remotes)

	var asked int
	err := c.query("account", func(client.Context) error {
		asked++
		if asked == 1 {
			return errors.New("307 temporary redirect")
		}
		return nil
	})
	if err != nil || asked != 2 {
		t.Fatalf("the second remote answered: err %v after %d remotes, want nil after 2", err, asked)
	}
	if out := log.String(); !strings.Contains(out, "Query failed") || !strings.Contains(out, "a.example") ||
		!strings.Contains(out, "307 temporary redirect") || strings.Contains(out, "b.example") {
		t.Fatalf("the failed remote, and only it, must be logged with its error:\n%s", out)
	}

	err = c.eachRemote("Broadcast failed", func(remote string) error { return fmt.Errorf("refused by %s", remote) })
	for _, r := range remotes {
		if err == nil || !strings.Contains(err.Error(), r+": refused by "+r) {
			t.Fatalf("every remote failed: the error must name each with its own error, got %v", err)
		}
	}
}

// TestARemoteConnectionIsReused: queries to a remote reuse one connection
// rather than open a new one, and a TLS handshake, each. A handshake costs
// the node up to three queries and a report pass one per session.
//
// Rules: [CH-10].
func TestARemoteConnectionIsReused(t *testing.T) {
	chain, c := newFakeChain(t)
	for i := 0; i < 10; i++ {
		if _, err := c.QueryAccount(c.FromAddress()); err != nil {
			t.Fatal(err)
		}
	}
	if n := chain.conns.Load(); n != 1 {
		t.Fatalf("10 queries opened %d connections, want 1", n)
	}

	// Handshakes query at the same time; they share the remote's client.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.QueryAccount(c.FromAddress()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

// TestQueriesLogAtDebugOnly: a handshake costs up to three queries and a
// session pass one per session; at the default log level they add nothing
// to the journal, a client's account address included.
func TestQueriesLogAtDebugOnly(t *testing.T) {
	_, c := newFakeChain(t)
	var out bytes.Buffer
	c.WithLogger(cmtlog.NewFilter(cmtlog.NewTMLogger(&out), cmtlog.AllowInfo()))

	if _, err := c.QueryAccount(c.FromAddress()); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 0 {
		t.Fatalf("a query logged at info:\n%s", out.String())
	}
}
