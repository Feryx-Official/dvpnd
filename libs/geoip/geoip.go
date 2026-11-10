// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package geoip

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/libs/geoip/types"
)

// Provider names accepted in the [geoip] config section.
const (
	// ProviderAuto asks ipwho.is, then ip2location.io, until one returns a full
	// location, cross-checks the country against Cloudflare, and falls back to
	// Cloudflare (country only), ipify (IP only) and finally node.ipv4_address.
	ProviderAuto        = "auto"
	ProviderIPWhois     = "ipwhois"     // https://ipwho.is — keyless, commercial use allowed on the free plan
	ProviderIP2Location = "ip2location" // https://api.ip2location.io — keyless 1000/day, or a free key (token)
	ProviderCloudflare  = "cloudflare"  // IP and country code only
	ProviderIPify       = "ipify"       // public IP only; https://www.ipify.org — open source, no stated limits
	ProviderIPAPI       = "ip-api"      // IP + city/country/coordinates; free tier is for non-commercial use only
	ProviderIPInfo      = "ipinfo"      // IP + country (free Lite plan); token required
	ProviderNone        = "none"        // no request; IP comes from node.ipv4_address

	// SourceStatic is reported when at least one static config value was applied.
	SourceStatic = "static"
)

// defaultURLs is a variable so tests can point every provider at a local server.
var defaultURLs = map[string]string{
	ProviderIPWhois:     "https://ipwho.is/",
	ProviderIP2Location: "https://api.ip2location.io/",
	ProviderCloudflare:  "https://www.cloudflare.com/cdn-cgi/trace",
	ProviderIPify:       "https://api.ipify.org?format=json",
	ProviderIPAPI:       "http://ip-api.com/json", // the free tier does not serve HTTPS
	ProviderIPInfo:      "https://api.ipinfo.io/lite/me",
}

// autoChain lists the city-level sources ProviderAuto tries, in order.
var autoChain = []string{ProviderIPWhois, ProviderIP2Location}

// timeout bounds each request; a test lowers it.
var timeout = 15 * time.Second

// Logger is the subset of the node logger the lookup reports through:
// fallbacks at Info, contradictions between sources at Error.
type Logger interface {
	Info(msg string, keyVals ...interface{})
	Error(msg string, keyVals ...interface{})
}

type nopLogger struct{}

func (nopLogger) Info(string, ...interface{})  {}
func (nopLogger) Error(string, ...interface{}) {}

// Options selects the lookup service and any operator-supplied values.
type Options struct {
	Provider string
	URL      string // optional endpoint override; ignored by ProviderAuto
	Token    string // API token: Bearer for ipinfo, ?key= for ip2location; never sent elsewhere
	IP       string // used as the address when Provider is "none", and as the last resort of "auto"
	Logger   Logger // nil means silent

	// Static values override whatever the provider returned, when set.
	City      string
	Country   string
	Latitude  float64
	Longitude float64
}

type provider struct {
	source string                                // reported in GeoIPLocation.Source and in log lines
	auth   func(req *http.Request, token string) // nil: the token is never sent to this provider
	parse  func(r io.Reader) (*types.GeoIPLocation, error)
}

var providers = map[string]provider{
	ProviderIPWhois:     {source: "ipwho.is", parse: parseIPWhois},
	ProviderIP2Location: {source: "ip2location.io", auth: queryKey, parse: parseIP2Location},
	ProviderCloudflare:  {source: "cloudflare", parse: parseCloudflare},
	ProviderIPify:       {source: "ipify", parse: parseIPify},
	ProviderIPAPI:       {source: "ip-api", parse: parseIPAPI},
	ProviderIPInfo:      {source: "ipinfo", auth: bearer, parse: parseIPInfo},
}

func bearer(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

func queryKey(req *http.Request, token string) {
	q := req.URL.Query()
	q.Set("key", token)
	req.URL.RawQuery = q.Encode()
}

// Location discovers the node's public address and, depending on the provider,
// its location, then applies the static overrides from opts.
func Location(opts Options) (*types.GeoIPLocation, error) {
	log := opts.Logger
	if log == nil {
		log = nopLogger{}
	}

	var (
		loc *types.GeoIPLocation
		err error
	)

	switch opts.Provider {
	case ProviderNone:
		loc = &types.GeoIPLocation{IP: opts.IP, Source: ProviderNone}
	case ProviderAuto:
		loc, err = lookupAuto(opts, log)
	default:
		if _, ok := providers[opts.Provider]; !ok {
			return nil, fmt.Errorf("unknown geoip provider %q", opts.Provider)
		}
		loc, err = fetch(opts.Provider, opts.URL, opts.Token)
	}
	if err != nil {
		return nil, err
	}

	applyStatic(loc, opts, log)
	return loc, nil
}

// lookupAuto walks the free, commercially usable sources: one request each, no
// retries. It returns the first full location, cross-checked against Cloudflare,
// and degrades to a country-only or IP-only answer before giving up.
func lookupAuto(opts Options, log Logger) (*types.GeoIPLocation, error) {
	var loc *types.GeoIPLocation
	for _, name := range autoChain {
		l, err := fetch(name, "", "")
		switch {
		case err != nil:
			log.Info("GeoIP source failed, trying the next one", "source", providers[name].source, "error", err)
		case l.CountryCode == "":
			log.Info("GeoIP source returned no country, trying the next one", "source", l.Source)
		default:
			loc = l
		}
		if loc != nil {
			break
		}
	}

	cf, cfErr := fetch(ProviderCloudflare, "", "")
	if loc != nil {
		switch {
		case cfErr != nil:
			log.Info("GeoIP country cross-check skipped", "source", providers[ProviderCloudflare].source, "error", cfErr)
		case cf.CountryCode != "" && cf.CountryCode != loc.CountryCode:
			log.Error("GeoIP sources disagree on the country; keeping the first answer",
				"source", loc.Source, "country_code", loc.CountryCode, "ip", loc.IP,
				"check_source", cf.Source, "check_country_code", cf.CountryCode, "check_ip", cf.IP)
		}
		return loc, nil
	}
	if cfErr == nil {
		log.Error("No GeoIP source returned a city; reporting the country only", "source", cf.Source)
		return cf, nil
	}

	l, err := fetch(ProviderIPify, "", "")
	if err == nil {
		log.Error("No GeoIP source returned a location; reporting the IP only", "source", l.Source)
		return l, nil
	}
	log.Info("GeoIP source failed", "source", providers[ProviderIPify].source, "error", err)

	if opts.IP != "" {
		log.Error("Every GeoIP source failed; using node.ipv4_address", "ip", opts.IP)
		return &types.GeoIPLocation{IP: opts.IP, Source: ProviderNone}, nil
	}
	return nil, errors.New("every geoip source failed and node.ipv4_address is not set")
}

// applyStatic replaces looked-up values with the operator's static ones and
// reports every contradiction, so an operator sees when clients that geolocate
// the node's IP will disagree with what the node claims.
func applyStatic(loc *types.GeoIPLocation, opts Options, log Logger) {
	static := false

	if opts.Country != "" {
		same := strings.EqualFold(opts.Country, loc.Country) || strings.EqualFold(opts.Country, loc.CountryCode)
		if loc.Country != "" && !same {
			log.Error("Static country differs from the looked-up one; using the static value",
				"source", loc.Source, "looked_up", loc.Country, "looked_up_code", loc.CountryCode, "static", opts.Country)
		}
		loc.Country = opts.Country
		switch {
		case len(opts.Country) == 2:
			loc.CountryCode = strings.ToUpper(opts.Country)
		case !same:
			loc.CountryCode = "" // never advertise a code that contradicts the name
		}
		static = true
	}

	if opts.City != "" {
		if loc.City != "" && !strings.EqualFold(opts.City, loc.City) {
			log.Error("Static city differs from the looked-up one; using the static value",
				"source", loc.Source, "looked_up", loc.City, "static", opts.City)
		}
		loc.City = opts.City
		static = true
	}

	if opts.Latitude != 0 || opts.Longitude != 0 {
		loc.Latitude, loc.Longitude = opts.Latitude, opts.Longitude
		static = true
	}

	if static {
		loc.Source = SourceStatic
	}
}

// client makes every request over IPv4, so the address a service echoes back
// is the one clients connect to (the node advertises an IPv4 address).
func client() *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp4", addr)
			},
			TLSHandshakeTimeout: timeout,
		},
	}
}

func fetch(name, url, token string) (*types.GeoIPLocation, error) {
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown geoip provider %q", name)
	}
	if url == "" {
		url = defaultURLs[name]
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" && p.auth != nil {
		p.auth(req, token)
	}

	resp, err := client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geoip source %s returned HTTP %d", p.source, resp.StatusCode)
	}

	loc, err := p.parse(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("geoip source %s: %w", p.source, err)
	}
	loc.Source = p.source
	return loc, nil
}

// parse decodes one provider's response body into the common location type.
func parse(name string, r io.Reader) (*types.GeoIPLocation, error) {
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown geoip provider %q", name)
	}
	return p.parse(r)
}

func parseIPWhois(r io.Reader) (*types.GeoIPLocation, error) {
	var body struct {
		IP          string  `json:"ip"`
		Success     bool    `json:"success"`
		Message     string  `json:"message"`
		City        string  `json:"city"`
		Country     string  `json:"country"`
		CountryCode string  `json:"country_code"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	if !body.Success { // failures come back as HTTP 200 with success:false
		return nil, fmt.Errorf("lookup failed: %s", body.Message)
	}
	return &types.GeoIPLocation{
		IP: body.IP, City: body.City, Country: body.Country, CountryCode: body.CountryCode,
		Latitude: body.Latitude, Longitude: body.Longitude,
	}, nil
}

func parseIP2Location(r io.Reader) (*types.GeoIPLocation, error) {
	var body struct {
		IP          string  `json:"ip"`
		City        string  `json:"city_name"`
		Country     string  `json:"country_name"`
		CountryCode string  `json:"country_code"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		Error       *struct {
			Code    int    `json:"error_code"`
			Message string `json:"error_message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	if body.Error != nil {
		return nil, fmt.Errorf("lookup failed: %d %s", body.Error.Code, body.Error.Message)
	}
	return &types.GeoIPLocation{
		IP: body.IP, City: body.City, Country: body.Country, CountryCode: body.CountryCode,
		Latitude: body.Latitude, Longitude: body.Longitude,
	}, nil
}

// parseCloudflare reads the key=value lines of /cdn-cgi/trace.
func parseCloudflare(r io.Reader) (*types.GeoIPLocation, error) {
	loc := &types.GeoIPLocation{}
	s := bufio.NewScanner(r)
	for s.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(s.Text()), "=")
		if !ok {
			continue
		}
		switch key {
		case "ip":
			loc.IP = value
		case "loc":
			if value != "" && value != "XX" { // XX: Cloudflare could not place the address
				loc.CountryCode = strings.ToUpper(value)
			}
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if loc.IP == "" {
		return nil, errors.New("no ip= line in the response")
	}
	return loc, nil
}

func parseIPify(r io.Reader) (*types.GeoIPLocation, error) {
	var body struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	return &types.GeoIPLocation{IP: body.IP}, nil
}

func parseIPAPI(r io.Reader) (*types.GeoIPLocation, error) {
	var body struct {
		City        string  `json:"city"`
		Country     string  `json:"country"`
		CountryCode string  `json:"countryCode"`
		IP          string  `json:"query"`
		Latitude    float64 `json:"lat"`
		Longitude   float64 `json:"lon"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	return &types.GeoIPLocation{
		City: body.City, Country: body.Country, CountryCode: body.CountryCode, IP: body.IP,
		Latitude: body.Latitude, Longitude: body.Longitude,
	}, nil
}

// parseIPInfo accepts both the Lite shape (country name + country_code) and the
// classic one (city, two-letter country, "lat,lon" loc).
func parseIPInfo(r io.Reader) (*types.GeoIPLocation, error) {
	var body struct {
		IP          string `json:"ip"`
		City        string `json:"city"`
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
		Loc         string `json:"loc"` // "lat,lon"
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	loc := &types.GeoIPLocation{IP: body.IP, City: body.City, Country: body.Country, CountryCode: body.CountryCode}
	if loc.CountryCode == "" && len(loc.Country) == 2 {
		loc.CountryCode = strings.ToUpper(loc.Country)
	}
	if parts := strings.SplitN(body.Loc, ",", 2); len(parts) == 2 {
		loc.Latitude, _ = strconv.ParseFloat(parts[0], 64)
		loc.Longitude, _ = strconv.ParseFloat(parts[1], 64)
	}
	return loc, nil
}
