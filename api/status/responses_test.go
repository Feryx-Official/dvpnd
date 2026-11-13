// SPDX-License-Identifier: Apache-2.0

package status

import (
	"encoding/json"
	"testing"
)

// The root document must carry the keys current client apps and aggregators
// read, in the layout nodes on the network answer with.
//
// Rules: [CT-2].
func TestRootDocumentLayout(t *testing.T) {
	doc := &ResponseGetRoot{
		Addr:            "sentnode1example",
		Downlink:        "1000000000",
		Uplink:          "500000000",
		HandshakeDNS:    false,
		Location:        &RootLocation{City: "Helsinki", Country: "Finland", CountryCode: "FI", Latitude: 60.2, Longitude: 24.9},
		Moniker:         "node",
		Peers:           1,
		ServiceType:     "hysteria2",
		ServiceMetadata: []map[string]interface{}{{"port": 0, "tls_pin": "", "obfs_password": ""}},
		Version:         &RootVersion{Name: "dvpnd", Tag: "0.7.1", Commit: "abc"},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"addr", "downlink", "handshake_dns", "location", "moniker", "peers", "service_type", "service_metadata", "uplink", "version"} {
		if _, ok := got[key]; !ok {
			t.Errorf("root document lacks %q: %s", key, out)
		}
	}
	for _, legacy := range []string{"address", "bandwidth", "type", "operator", "qos", "gigabyte_prices"} {
		if _, ok := got[legacy]; ok {
			t.Errorf("root document must not carry the legacy key %q", legacy)
		}
	}
	version := got["version"].(map[string]interface{})
	if version["name"] != "dvpnd" || version["tag"] != "0.7.1" || version["commit"] != "abc" {
		t.Fatalf("version: %v", version)
	}
	location := got["location"].(map[string]interface{})
	if _, ok := location["source"]; ok {
		t.Fatal("location must not carry the source")
	}
}

// The legacy status document says where its bandwidth figure came from, the
// way its location says which service geolocated the node.
//
// Rules: [CT-2].
func TestStatusBandwidthCarriesSource(t *testing.T) {
	out, err := json.Marshal(ResponseGetStatus{
		Bandwidth: &Bandwidth{Download: 125000000, Upload: 125000000, Source: "config"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	bandwidth := got["bandwidth"].(map[string]interface{})
	if bandwidth["source"] != "config" || bandwidth["download"] != float64(125000000) {
		t.Fatalf("bandwidth: %v", bandwidth)
	}
}
