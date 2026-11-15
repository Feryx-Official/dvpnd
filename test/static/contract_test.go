// SPDX-License-Identifier: Apache-2.0

package static

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/bech32"
)

// TestTheChainContractCopyIsTheChains: context/testdata/chain-contract.json,
// which the node's messages are checked against, is FeryxHub's frozen wire
// and signer contract for those messages, entry for entry, as the chain's
// Phase 3 fixture holds it in the library go.mod requires (read from the
// module cache with the network off). DVPND_CHAIN_CONTRACTS names another
// copy of that fixture to check against instead, such as the one of a chain
// built on a newer SDK while the node keeps this library.
//
// Rules: [CH-14].
func TestTheChainContractCopyIsTheChains(t *testing.T) {
	path := os.Getenv("DVPND_CHAIN_CONTRACTS")
	if path == "" {
		cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/Feryx-Official/feryxhub/v12")
		cmd.Dir = root(t)
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v (the module cache must hold the dependencies: go mod download)\n%s", err, out)
		}
		path = filepath.Join(strings.TrimSpace(string(out)), "x", "vpn", "keeper", "testdata", "phase3", "contracts.json")
	}

	type entry struct {
		TypeURL, From, Wire        string
		Routed                     bool
		Signers                    []string
		AcceptedHRPs, RejectedHRPs []string
	}
	var chain, node struct{ Messages []entry }
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &chain); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(read(t, "context/testdata/chain-contract.json"), &node); err != nil {
		t.Fatal(err)
	}
	if len(node.Messages) != 4 {
		t.Fatalf("the node's copy holds %d messages, want the four it sends", len(node.Messages))
	}

	byURL := map[string]entry{}
	for _, e := range chain.Messages {
		byURL[e.TypeURL] = e
	}
	for _, mine := range node.Messages {
		theirs, ok := byURL[mine.TypeURL]
		if !ok {
			t.Errorf("%s: the chain's contract has no such message", mine.TypeURL)
			continue
		}
		from := theirs.From
		theirs.From = ""
		if !reflect.DeepEqual(mine, theirs) {
			t.Errorf("%s: the node's copy\n  %+v\nthe chain's\n  %+v", mine.TypeURL, mine, theirs)
			continue
		}
		signer, err := hex.DecodeString(mine.Signers[0])
		if err != nil {
			t.Fatal(err)
		}
		if want, err := bech32.ConvertAndEncode(mine.AcceptedHRPs[0], signer); err != nil || from != want {
			t.Errorf("%s: the chain built its vector for %s, not the signer under %s (%v)", mine.TypeURL, from, mine.AcceptedHRPs[0], err)
		}
	}
}
