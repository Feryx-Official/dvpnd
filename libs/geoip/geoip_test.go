// SPDX-License-Identifier: Apache-2.0

package geoip

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/libs/geoip/types"
)

// recorder captures log lines so tests can assert on what the operator would see.
type recorder struct {
	mu     sync.Mutex
	infos  []string
	errors []string
}

func (r *recorder) Info(msg string, kv ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, msg+" "+fmt.Sprint(kv...))
}

func (r *recorder) Error(msg string, kv ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, msg+" "+fmt.Sprint(kv...))
}

// reply is what the fake service answers for one provider.
type reply struct {
	status int
	body   string
	delay  time.Duration
}

// fake stands in for every provider: one path per provider name on a local
// server, with a hit counter and a configurable reply each.
type fake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	replies map[string]reply
	hits    map[string]int
	auth    map[string]string // last Authorization header / key query seen per provider
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{replies: map[string]reply{}, hits: map[string]int{}, auth: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		f.mu.Lock()
		f.hits[name]++
		f.auth[name] = r.Header.Get("Authorization") + "|" + r.URL.Query().Get("key")
		rep, ok := f.replies[name]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rep.delay > 0 {
			time.Sleep(rep.delay)
		}
		if rep.status == 0 {
			rep.status = http.StatusOK
		}
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(f.srv.Close)

	urls := map[string]string{}
	for name := range providers {
		urls[name] = f.srv.URL + "/" + name
	}
	stubURLs(t, urls)
	return f
}

func (f *fake) set(name string, r reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[name] = r
}

func (f *fake) hit(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[name]
}

func stubURLs(t *testing.T, urls map[string]string) {
	t.Helper()
	saved := defaultURLs
	defaultURLs = urls
	t.Cleanup(func() { defaultURLs = saved })
}

const (
	bodyIPWhois     = `{"ip":"203.0.113.7","success":true,"city":"Milan","country":"Italy","country_code":"IT","latitude":45.46,"longitude":9.19}`
	bodyIP2Location = `{"ip":"203.0.113.7","country_code":"IT","country_name":"Italy","city_name":"Milan","latitude":45.46,"longitude":9.19}`
	bodyCloudflare  = "fl=1a2b\nh=www.cloudflare.com\nip=203.0.113.7\nts=1.0\ncolo=MXP\nloc=IT\ntls=TLSv1.3\n"
	bodyIPify       = `{"ip":"203.0.113.7"}`
)

// Rules: [CT-13].
func TestParse(t *testing.T) {
	cases := []struct {
		name, provider, body string
		want                 types.GeoIPLocation
	}{
		{"ipify", ProviderIPify, bodyIPify, types.GeoIPLocation{IP: "203.0.113.7"}},
		{"ipwhois", ProviderIPWhois, bodyIPWhois,
			types.GeoIPLocation{IP: "203.0.113.7", City: "Milan", Country: "Italy", CountryCode: "IT", Latitude: 45.46, Longitude: 9.19}},
		{"ip2location", ProviderIP2Location, bodyIP2Location,
			types.GeoIPLocation{IP: "203.0.113.7", City: "Milan", Country: "Italy", CountryCode: "IT", Latitude: 45.46, Longitude: 9.19}},
		{"cloudflare", ProviderCloudflare, bodyCloudflare, types.GeoIPLocation{IP: "203.0.113.7", CountryCode: "IT"}},
		{"cloudflare unplaced", ProviderCloudflare, "ip=203.0.113.7\nloc=XX\n", types.GeoIPLocation{IP: "203.0.113.7"}},
		{"ip-api", ProviderIPAPI,
			`{"status":"success","country":"Italy","countryCode":"IT","city":"Milan","lat":45.46,"lon":9.19,"query":"203.0.113.7"}`,
			types.GeoIPLocation{IP: "203.0.113.7", City: "Milan", Country: "Italy", CountryCode: "IT", Latitude: 45.46, Longitude: 9.19}},
		{"ipinfo classic", ProviderIPInfo, `{"ip":"203.0.113.7","city":"Milan","country":"IT","loc":"45.4600,9.1900"}`,
			types.GeoIPLocation{IP: "203.0.113.7", City: "Milan", Country: "IT", CountryCode: "IT", Latitude: 45.46, Longitude: 9.19}},
		{"ipinfo lite", ProviderIPInfo, `{"ip":"203.0.113.7","asn":"AS64496","country_code":"IT","country":"Italy","continent":"Europe"}`,
			types.GeoIPLocation{IP: "203.0.113.7", Country: "Italy", CountryCode: "IT"}},
	}
	for _, c := range cases {
		loc, err := parse(c.provider, strings.NewReader(c.body))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if *loc != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, *loc, c.want)
		}
	}

	failures := []struct{ name, provider, body, want string }{
		{"ipwhois failure", ProviderIPWhois, `{"ip":"0.0.0.0","success":false,"message":"Reserved range"}`, "Reserved range"},
		{"ip2location failure", ProviderIP2Location, `{"error":{"error_code":10001,"error_message":"Invalid API key"}}`, "Invalid API key"},
		{"cloudflare without ip", ProviderCloudflare, "loc=IT\ncolo=MXP\n", "no ip="},
		{"unknown provider", "bogus", `{}`, "unknown geoip provider"},
		{"malformed json", ProviderIPWhois, `{`, "unexpected EOF"},
	}
	for _, c := range failures {
		_, err := parse(c.provider, strings.NewReader(c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want error containing %q", c.name, err, c.want)
		}
	}
}

// Rules: [CT-13].
func TestFetch(t *testing.T) {
	f := newFake(t)
	f.set(ProviderIPInfo, reply{body: `{"ip":"203.0.113.7","country":"Italy","country_code":"IT"}`})
	f.set(ProviderIP2Location, reply{body: bodyIP2Location})
	f.set(ProviderIPify, reply{body: bodyIPify})
	f.set(ProviderIPWhois, reply{status: http.StatusTooManyRequests, body: `{}`})

	loc, err := fetch(ProviderIPInfo, "", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Source != "ipinfo" || loc.CountryCode != "IT" {
		t.Errorf("ipinfo: got %+v", *loc)
	}
	if got := f.auth[ProviderIPInfo]; got != "Bearer secret|" {
		t.Errorf("ipinfo must receive the token as a Bearer header, got %q", got)
	}

	if _, err := fetch(ProviderIP2Location, "", "secret"); err != nil {
		t.Fatal(err)
	}
	if got := f.auth[ProviderIP2Location]; got != "|secret" {
		t.Errorf("ip2location must receive the token as ?key=, got %q", got)
	}

	if _, err := fetch(ProviderIPify, "", "secret"); err != nil {
		t.Fatal(err)
	}
	if got := f.auth[ProviderIPify]; got != "|" {
		t.Errorf("ipify must never receive the token, got %q", got)
	}

	if _, err := fetch(ProviderIPWhois, "", ""); err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("non-200 must fail with the status, got %v", err)
	}
	if _, err := fetch("bogus", "", ""); err == nil {
		t.Error("unknown provider must fail")
	}
}

// Rules: [CT-13].
func TestLocationAuto(t *testing.T) {
	full := types.GeoIPLocation{IP: "203.0.113.7", City: "Milan", Country: "Italy", CountryCode: "IT", Latitude: 45.46, Longitude: 9.19}

	run := func(t *testing.T, setup func(f *fake), ip string) (*fake, *recorder, *types.GeoIPLocation, error) {
		t.Helper()
		f := newFake(t)
		f.set(ProviderIPWhois, reply{body: bodyIPWhois})
		f.set(ProviderIP2Location, reply{body: bodyIP2Location})
		f.set(ProviderCloudflare, reply{body: bodyCloudflare})
		f.set(ProviderIPify, reply{body: bodyIPify})
		setup(f)
		rec := &recorder{}
		loc, err := Location(Options{Provider: ProviderAuto, IP: ip, Logger: rec})
		for name := range providers {
			if n := f.hit(name); n > 1 {
				t.Errorf("%s was queried %d times, want at most once", name, n)
			}
		}
		return f, rec, loc, err
	}

	t.Run("primary answers", func(t *testing.T) {
		f, rec, loc, err := run(t, func(*fake) {}, "")
		if err != nil {
			t.Fatal(err)
		}
		want := full
		want.Source = "ipwho.is"
		if *loc != want {
			t.Errorf("got %+v, want %+v", *loc, want)
		}
		if f.hit(ProviderIP2Location) != 0 || f.hit(ProviderIPify) != 0 || f.hit(ProviderCloudflare) != 1 {
			t.Errorf("unexpected requests: %v", f.hits)
		}
		if len(rec.errors) != 0 {
			t.Errorf("no error expected, got %v", rec.errors)
		}
	})

	fallbacks := map[string]reply{
		"primary HTTP 500":        {status: http.StatusInternalServerError, body: "{}"},
		"primary success false":   {body: `{"success":false,"message":"nope"}`},
		"primary without country": {body: `{"ip":"203.0.113.7","success":true,"city":"Milan"}`},
	}
	for name, rep := range fallbacks {
		t.Run(name, func(t *testing.T) {
			f, rec, loc, err := run(t, func(f *fake) { f.set(ProviderIPWhois, rep) }, "")
			if err != nil {
				t.Fatal(err)
			}
			if loc.Source != "ip2location.io" || loc.CountryCode != "IT" || loc.City != "Milan" {
				t.Errorf("got %+v", *loc)
			}
			if f.hit(ProviderIP2Location) != 1 || len(rec.infos) == 0 || len(rec.errors) != 0 {
				t.Errorf("hits %v infos %v errors %v", f.hits, rec.infos, rec.errors)
			}
		})
	}

	t.Run("country only", func(t *testing.T) {
		_, rec, loc, err := run(t, func(f *fake) {
			f.set(ProviderIPWhois, reply{status: 503})
			f.set(ProviderIP2Location, reply{status: 503})
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		want := types.GeoIPLocation{IP: "203.0.113.7", CountryCode: "IT", Source: "cloudflare"}
		if *loc != want {
			t.Errorf("got %+v, want %+v", *loc, want)
		}
		if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "country only") {
			t.Errorf("want one country-only error, got %v", rec.errors)
		}
	})

	t.Run("ip only", func(t *testing.T) {
		_, rec, loc, err := run(t, func(f *fake) {
			f.set(ProviderIPWhois, reply{status: 503})
			f.set(ProviderIP2Location, reply{status: 503})
			f.set(ProviderCloudflare, reply{status: 503})
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		want := types.GeoIPLocation{IP: "203.0.113.7", Source: "ipify"}
		if *loc != want {
			t.Errorf("got %+v, want %+v", *loc, want)
		}
		if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "IP only") {
			t.Errorf("want one ip-only error, got %v", rec.errors)
		}
	})

	allDown := func(f *fake) {
		for name := range providers {
			f.set(name, reply{status: 503})
		}
	}
	t.Run("everything down without ipv4_address", func(t *testing.T) {
		_, _, _, err := run(t, allDown, "")
		if err == nil || !strings.Contains(err.Error(), "ipv4_address") {
			t.Errorf("want a fatal error naming ipv4_address, got %v", err)
		}
	})
	t.Run("everything down with ipv4_address", func(t *testing.T) {
		_, rec, loc, err := run(t, allDown, "198.51.100.9")
		if err != nil {
			t.Fatal(err)
		}
		want := types.GeoIPLocation{IP: "198.51.100.9", Source: ProviderNone}
		if *loc != want || len(rec.errors) != 1 {
			t.Errorf("got %+v errors %v", *loc, rec.errors)
		}
	})

	t.Run("country mismatch is reported and the first answer kept", func(t *testing.T) {
		_, rec, loc, err := run(t, func(f *fake) {
			f.set(ProviderCloudflare, reply{body: "ip=203.0.113.7\nloc=DE\n"})
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		if loc.CountryCode != "IT" || loc.Source != "ipwho.is" {
			t.Errorf("got %+v", *loc)
		}
		if len(rec.errors) != 1 {
			t.Fatalf("want exactly one error, got %v", rec.errors)
		}
		for _, s := range []string{"ipwho.is", "IT", "cloudflare", "DE"} {
			if !strings.Contains(rec.errors[0], s) {
				t.Errorf("error line %q must name %s", rec.errors[0], s)
			}
		}
	})

	t.Run("cross-check outage is not an error", func(t *testing.T) {
		_, rec, loc, err := run(t, func(f *fake) { f.set(ProviderCloudflare, reply{status: 503}) }, "")
		if err != nil {
			t.Fatal(err)
		}
		if loc.Source != "ipwho.is" || len(rec.errors) != 0 || len(rec.infos) != 1 {
			t.Errorf("got %+v errors %v infos %v", *loc, rec.errors, rec.infos)
		}
	})

	t.Run("slow source is skipped", func(t *testing.T) {
		saved := timeout
		timeout = 200 * time.Millisecond
		t.Cleanup(func() { timeout = saved })

		start := time.Now()
		_, _, loc, err := run(t, func(f *fake) { f.set(ProviderIPWhois, reply{body: bodyIPWhois, delay: 2 * time.Second}) }, "")
		if err != nil {
			t.Fatal(err)
		}
		if loc.Source != "ip2location.io" {
			t.Errorf("got %+v", *loc)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("the chain waited %s for a slow source", took)
		}
	})
}

// Rules: [CT-13].
func TestLocationStaticOverrides(t *testing.T) {
	t.Run("none with every static field", func(t *testing.T) {
		loc, err := Location(Options{Provider: ProviderNone, IP: "198.51.100.9", City: "Turin", Country: "Italy", Latitude: 45.07, Longitude: 7.69})
		if err != nil {
			t.Fatal(err)
		}
		want := types.GeoIPLocation{IP: "198.51.100.9", City: "Turin", Country: "Italy", Latitude: 45.07, Longitude: 7.69, Source: SourceStatic}
		if *loc != want {
			t.Errorf("got %+v, want %+v", *loc, want)
		}
	})

	t.Run("no static values keep the source", func(t *testing.T) {
		loc, err := Location(Options{Provider: ProviderNone, IP: "198.51.100.9"})
		if err != nil {
			t.Fatal(err)
		}
		if loc.Source != ProviderNone {
			t.Errorf("got %+v", *loc)
		}
	})

	lookedUp := `{"ip":"203.0.113.7","success":true,"city":"Berlin","country":"Germany","country_code":"DE","latitude":52.52,"longitude":13.4}`

	t.Run("contradicting country name is logged and clears the code", func(t *testing.T) {
		f := newFake(t)
		f.set(ProviderIPWhois, reply{body: lookedUp})
		rec := &recorder{}
		loc, err := Location(Options{Provider: ProviderIPWhois, Country: "Italy", Logger: rec})
		if err != nil {
			t.Fatal(err)
		}
		if loc.Country != "Italy" || loc.CountryCode != "" || loc.City != "Berlin" || loc.Source != SourceStatic {
			t.Errorf("got %+v", *loc)
		}
		if len(rec.errors) != 1 {
			t.Fatalf("want one error, got %v", rec.errors)
		}
		for _, s := range []string{"ipwho.is", "Germany", "Italy"} {
			if !strings.Contains(rec.errors[0], s) {
				t.Errorf("error line %q must name %s", rec.errors[0], s)
			}
		}
	})

	t.Run("two-letter static country sets the code", func(t *testing.T) {
		f := newFake(t)
		f.set(ProviderIPWhois, reply{body: lookedUp})
		rec := &recorder{}
		loc, err := Location(Options{Provider: ProviderIPWhois, Country: "it", Logger: rec})
		if err != nil {
			t.Fatal(err)
		}
		if loc.Country != "it" || loc.CountryCode != "IT" || loc.Source != SourceStatic || len(rec.errors) != 1 {
			t.Errorf("got %+v errors %v", *loc, rec.errors)
		}
	})

	t.Run("matching static country is silent", func(t *testing.T) {
		f := newFake(t)
		f.set(ProviderIPWhois, reply{body: lookedUp})
		rec := &recorder{}
		loc, err := Location(Options{Provider: ProviderIPWhois, Country: "germany", Logger: rec})
		if err != nil {
			t.Fatal(err)
		}
		if loc.CountryCode != "DE" || loc.Source != SourceStatic || len(rec.errors) != 0 {
			t.Errorf("got %+v errors %v", *loc, rec.errors)
		}
	})

	t.Run("contradicting city is logged", func(t *testing.T) {
		f := newFake(t)
		f.set(ProviderIPWhois, reply{body: lookedUp})
		rec := &recorder{}
		loc, err := Location(Options{Provider: ProviderIPWhois, City: "Munich", Logger: rec})
		if err != nil {
			t.Fatal(err)
		}
		if loc.City != "Munich" || loc.Country != "Germany" || loc.CountryCode != "DE" || loc.Source != SourceStatic {
			t.Errorf("got %+v", *loc)
		}
		if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "Berlin") {
			t.Errorf("want one error naming Berlin, got %v", rec.errors)
		}
	})

	t.Run("coordinates only", func(t *testing.T) {
		f := newFake(t)
		f.set(ProviderIPWhois, reply{body: lookedUp})
		rec := &recorder{}
		loc, err := Location(Options{Provider: ProviderIPWhois, Latitude: 48.14, Longitude: 11.58, Logger: rec})
		if err != nil {
			t.Fatal(err)
		}
		if loc.Latitude != 48.14 || loc.Longitude != 11.58 || loc.City != "Berlin" || loc.Source != SourceStatic || len(rec.errors) != 0 {
			t.Errorf("got %+v errors %v", *loc, rec.errors)
		}
	})

	if _, err := Location(Options{Provider: "bogus"}); err == nil {
		t.Error("unknown provider must fail")
	}
}

// Rules: [CT-13].
func TestLocationURLOverride(t *testing.T) {
	f := newFake(t)
	f.set("custom", reply{body: bodyIPWhois})
	loc, err := Location(Options{Provider: ProviderIPWhois, URL: f.srv.URL + "/custom"})
	if err != nil {
		t.Fatal(err)
	}
	if loc.Source != "ipwho.is" || f.hit("custom") != 1 || f.hit(ProviderIPWhois) != 0 {
		t.Errorf("got %+v hits %v", *loc, f.hits)
	}
}

// TestOnlyAPublicIPv4IsTheNodeAddress: the address a source returns is what
// clients are sent as the node's address. A source that answers with no
// address, an IPv6 address or one no client can reach counts as failed:
// "auto" moves on to the next source, a chosen provider fails the start.
// Without the check an empty or IPv6 answer reached clients as "<nil>".
//
// Rules: [CT-14].
func TestOnlyAPublicIPv4IsTheNodeAddress(t *testing.T) {
	for _, ip := range []string{"", "2001:db8::1", "127.0.0.1", "0.0.0.0", "255.255.255.255", "169.254.1.1", "224.0.0.1", "not-an-ip"} {
		t.Run("ipify answers "+ip, func(t *testing.T) {
			f := newFake(t)
			f.set(ProviderIPify, reply{body: `{"ip":"` + ip + `"}`})
			if loc, err := Location(Options{Provider: ProviderIPify}); err == nil {
				t.Fatalf("a chosen provider's %q became the node address: %+v", ip, *loc)
			}
		})

		t.Run("auto skips "+ip, func(t *testing.T) {
			f := newFake(t)
			f.set(ProviderIPWhois, reply{body: `{"ip":"` + ip + `","success":true,"city":"Milan","country":"Italy","country_code":"IT"}`})
			f.set(ProviderIP2Location, reply{body: bodyIP2Location})
			f.set(ProviderCloudflare, reply{body: "ip=" + ip + "\nloc=IT\n"})
			f.set(ProviderIPify, reply{body: bodyIPify})
			loc, err := Location(Options{Provider: ProviderAuto, Logger: &recorder{}})
			if err != nil {
				t.Fatal(err)
			}
			if loc.IP != "203.0.113.7" || loc.Source != "ip2location.io" {
				t.Fatalf("got %+v, want ip2location.io's answer", *loc)
			}
		})
	}

	t.Run("auto falls back past cloudflare", func(t *testing.T) {
		f := newFake(t)
		f.set(ProviderIPWhois, reply{status: http.StatusInternalServerError, body: "{}"})
		f.set(ProviderIP2Location, reply{status: http.StatusInternalServerError, body: "{}"})
		f.set(ProviderCloudflare, reply{body: "ip=2001:db8::1\nloc=IT\n"})
		f.set(ProviderIPify, reply{body: bodyIPify})
		loc, err := Location(Options{Provider: ProviderAuto, Logger: &recorder{}})
		if err != nil {
			t.Fatal(err)
		}
		if loc.IP != "203.0.113.7" || loc.Source != "ipify" {
			t.Fatalf("got %+v, want ipify's answer", *loc)
		}
	})

	t.Run("an IPv4-mapped answer reads as IPv4", func(t *testing.T) {
		f := newFake(t)
		f.set(ProviderIPify, reply{body: `{"ip":"::ffff:203.0.113.9"}`})
		loc, err := Location(Options{Provider: ProviderIPify})
		if err != nil {
			t.Fatal(err)
		}
		if loc.IP != "203.0.113.9" {
			t.Fatalf("IP %q, want 203.0.113.9", loc.IP)
		}
	})
}

// parse decodes one provider's response body into the common location type.
func parse(name string, r io.Reader) (*types.GeoIPLocation, error) {
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown geoip provider %q", name)
	}
	return p.parse(r)
}
