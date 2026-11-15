// SPDX-License-Identifier: Apache-2.0

package context

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/cosmos/gogoproto/proto"

	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// contractEntry is one message of FeryxHub's frozen wire and signer
// contract: its type URL; the wire bytes of the message with only its
// signer field set, to the signer bytes under the first accepted prefix;
// those signer bytes; and the address prefixes the chain accepts and
// refuses for it.
type contractEntry struct {
	TypeURL      string
	Wire         string
	Routed       bool
	Signers      []string
	AcceptedHRPs []string
	RejectedHRPs []string
}

func chainContract(t *testing.T) []contractEntry {
	t.Helper()
	b, err := os.ReadFile("testdata/chain-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Messages []contractEntry }
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}

	return c.Messages
}

// signers is GetSigners, or false where it panics, as it does for an address
// under a prefix the message does not take.
func signers(m sdk.Msg) (out []sdk.AccAddress, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()

	return m.GetSigners(), true
}

// withFrom is a copy of m with its signer field set to from and, when
// alone, every other field cleared, as the chain's address-only vectors are.
func withFrom(t *testing.T, m sdk.Msg, from string, alone bool) sdk.Msg {
	t.Helper()
	b, err := proto.Marshal(m) // a copy: proto.Clone cannot copy the chain's integers
	if err != nil {
		t.Fatal(err)
	}
	c := reflect.New(reflect.TypeOf(m).Elem()).Interface().(sdk.Msg)
	if err := proto.Unmarshal(b, c); err != nil {
		t.Fatal(err)
	}
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); v.Type().Field(i).Name == "From" {
			f.SetString(from)
		} else if alone && f.CanSet() {
			f.Set(reflect.Zero(f.Type()))
		}
	}

	return c
}

// TestMessagesMatchTheChainContract: every message the node sends, built the
// way the node builds it for an account, is the chain's frozen contract for
// its type: the type URL, the signer field under the prefix the chain takes,
// the wire bytes, the signer bytes, and a refusal under every other prefix.
// The node's codec packs each into an Any under that type URL.
//
// Rules: [CH-14].
func TestMessagesMatchTheChainContract(t *testing.T) {
	entries := chainContract(t)
	registry := lite.DefaultEncodingConfig().InterfaceRegistry

	var urls []string
	for _, e := range entries {
		urls = append(urls, e.TypeURL)
	}
	sort.Strings(urls)
	sent := append([]string(nil), lite.NodeMsgTypeURLs...)
	sort.Strings(sent)
	if !reflect.DeepEqual(urls, sent) {
		t.Fatalf("the contract holds %v; the node sends %v", urls, sent)
	}

	for _, e := range entries {
		signer, err := hex.DecodeString(e.Signers[0])
		if err != nil || len(e.Signers) != 1 {
			t.Fatalf("%s: signers %v", e.TypeURL, e.Signers)
		}
		config := types.NewConfig().WithDefaultValues()
		config.Node.RemoteURL = "https://127.0.0.1:8585"
		c := NewContext().WithConfig(config).WithClient(lite.NewDefaultClient().WithFromAddress(signer))
		built := map[string]sdk.Msg{}
		for _, m := range []sdk.Msg{c.registerNodeMsg(), c.nodeDetailsMsg(), c.nodeStatusMsg(),
			c.sessionReportMsg(types.Report{ID: 1, Download: 5, Upload: 7, Duration: time.Minute})} {
			built[sdk.MsgTypeURL(m)] = m
		}

		m, ok := built[e.TypeURL]
		if !ok || !e.Routed {
			t.Fatalf("%s: the node builds no such message, or the chain routes none", e.TypeURL)
		}
		got, ok := signers(m)
		if !ok || len(got) != 1 || !bytes.Equal(got[0], signer) {
			t.Fatalf("%s: signers %v, want the node account's", e.TypeURL, got)
		}
		from, err := bech32.ConvertAndEncode(e.AcceptedHRPs[0], signer)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.ValueOf(m).Elem().FieldByName("From").String() != from {
			t.Fatalf("%s: from %s, want the address under %s", e.TypeURL, reflect.ValueOf(m).Elem().FieldByName("From"), e.AcceptedHRPs[0])
		}
		wire, err := proto.Marshal(withFrom(t, m, from, true))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(wire) != e.Wire {
			t.Fatalf("%s: wire %x, want %s", e.TypeURL, wire, e.Wire)
		}
		for hrps, want := range map[*[]string]bool{&e.AcceptedHRPs: true, &e.RejectedHRPs: false} {
			for _, hrp := range *hrps {
				addr, err := bech32.ConvertAndEncode(hrp, signer)
				if err != nil {
					t.Fatal(err)
				}
				got, ok := signers(withFrom(t, m, addr, false))
				if accepted := ok && len(got) == 1 && bytes.Equal(got[0], signer); accepted != want {
					t.Errorf("%s: signer under %s accepted %v, want %v", e.TypeURL, hrp, accepted, want)
				}
			}
		}
		if _, err := registry.Resolve(e.TypeURL); err != nil {
			t.Errorf("%s: the node's codec cannot resolve it: %v", e.TypeURL, err)
		}
		if a, err := codectypes.NewAnyWithValue(m); err != nil || a.TypeUrl != e.TypeURL {
			t.Errorf("%s: packed as %v (%v)", e.TypeURL, a, err)
		}
	}
}
