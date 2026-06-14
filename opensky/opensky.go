// Package opensky is the library behind the opensky command line:
// the HTTP client, request shaping, and the typed data models for the
// OpenSky Network real-time flight tracking API.
//
// The Client paces requests to respect the anonymous rate limit (max 1 req/5s),
// sets a real User-Agent, adds HTTP Basic auth when credentials are supplied,
// and retries transient failures (429 and 5xx) with exponential backoff.
package opensky

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Host is the OpenSky Network API host.
const Host = "opensky-network.org"

// DefaultBaseURL is the root every request is built from.
const DefaultBaseURL = "https://opensky-network.org/api"

// DefaultUserAgent identifies this client to OpenSky.
const DefaultUserAgent = "opensky-cli/0.1.0 (github.com/tamnd/opensky-cli)"

// Config holds all tunable parameters for the Client.
type Config struct {
	BaseURL   string
	UserAgent string
	Username  string        // optional HTTP Basic auth
	Password  string        // optional HTTP Basic auth
	Rate      time.Duration // minimum gap between requests (anonymous: 5s)
	Timeout   time.Duration
	Retries   int
}

// DefaultConfig returns a Config with conservative defaults suitable for the
// anonymous tier (max 1 request per 5 seconds, 400 API credits per day).
func DefaultConfig() Config {
	return Config{
		BaseURL:   DefaultBaseURL,
		UserAgent: DefaultUserAgent,
		Rate:      5 * time.Second,
		Timeout:   30 * time.Second,
		Retries:   2,
	}
}

// Client talks to the OpenSky Network API over HTTPS.
type Client struct {
	http      *http.Client
	cfg       Config
	mu        sync.Mutex
	lastReq   time.Time
}

// NewClient returns a Client with default configuration.
func NewClient() *Client {
	cfg := DefaultConfig()
	return newClientFromConfig(cfg)
}

// NewClientWithConfig returns a Client configured from cfg.
func NewClientWithConfig(cfg Config) *Client {
	return newClientFromConfig(cfg)
}

func newClientFromConfig(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{
		http: &http.Client{Timeout: cfg.Timeout},
		cfg:  cfg,
	}
}

// FlightState holds the parsed state of one aircraft as broadcast by the
// OpenSky Network. Fields that the aircraft has not reported are nil.
type FlightState struct {
	ICAO24         string   `json:"icao24" kit:"id"`
	Callsign       string   `json:"callsign"`
	Country        string   `json:"origin_country"`
	TimePosition   *int64   `json:"time_position,omitempty"`
	LastContact    int64    `json:"last_contact"`
	Longitude      *float64 `json:"longitude,omitempty"`
	Latitude       *float64 `json:"latitude,omitempty"`
	BaroAltitude   *float64 `json:"baro_altitude,omitempty"`
	OnGround       bool     `json:"on_ground"`
	Velocity       *float64 `json:"velocity,omitempty"`       // m/s
	TrueTrack      *float64 `json:"true_track,omitempty"`     // degrees
	VerticalRate   *float64 `json:"vertical_rate,omitempty"`  // m/s
	GeoAltitude    *float64 `json:"geo_altitude,omitempty"`
	Squawk         string   `json:"squawk,omitempty"`
	SPI            bool     `json:"spi"`
	PositionSource int      `json:"position_source"` // 0=ADS-B, 1=ASTERIX, 2=MLAT, 3=FLARM
}

// StatesFilter holds the optional query parameters for States().
type StatesFilter struct {
	// BBox restricts to a geographic bounding box [minLat, minLon, maxLat, maxLon].
	BBox []float64
	// ICAO24 restricts to a specific aircraft transponder code.
	ICAO24 string
	// Time requests the state at a historical Unix timestamp (registered users only).
	Time int64
	// Limit caps the number of returned states. 0 = no limit.
	Limit int
}

// statesResponse is the raw JSON envelope from the OpenSky /states/all endpoint.
type statesResponse struct {
	Time   int64             `json:"time"`
	States []json.RawMessage `json:"states"`
}

// States fetches current (or historical) flight states from the OpenSky API.
func (c *Client) States(ctx context.Context, f StatesFilter) ([]*FlightState, error) {
	u, err := url.Parse(c.cfg.BaseURL + "/states/all")
	if err != nil {
		return nil, fmt.Errorf("opensky: build url: %w", err)
	}
	q := u.Query()
	if len(f.BBox) == 4 {
		q.Set("lamin", strconv.FormatFloat(f.BBox[0], 'f', -1, 64))
		q.Set("lomin", strconv.FormatFloat(f.BBox[1], 'f', -1, 64))
		q.Set("lamax", strconv.FormatFloat(f.BBox[2], 'f', -1, 64))
		q.Set("lomax", strconv.FormatFloat(f.BBox[3], 'f', -1, 64))
	}
	if f.ICAO24 != "" {
		q.Set("icao24", strings.ToLower(f.ICAO24))
	}
	if f.Time != 0 {
		q.Set("time", strconv.FormatInt(f.Time, 10))
	}
	u.RawQuery = q.Encode()

	body, err := c.get(ctx, u.String())
	if err != nil {
		return nil, err
	}

	var raw statesResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("opensky: decode response: %w", err)
	}

	out := make([]*FlightState, 0, len(raw.States))
	for _, elem := range raw.States {
		fs, err := parseStateArray(elem)
		if err != nil {
			continue // skip malformed entries
		}
		out = append(out, fs)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

// parseStateArray decodes one state vector from the mixed-type JSON array the
// OpenSky API returns. Each element is at a fixed index; nullable fields use
// JSON null which becomes a nil pointer.
func parseStateArray(raw json.RawMessage) (*FlightState, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, fmt.Errorf("state array: %w", err)
	}
	if len(elems) < 17 {
		return nil, fmt.Errorf("state array: got %d elements, need 17", len(elems))
	}

	fs := &FlightState{}

	// 0: icao24
	if err := json.Unmarshal(elems[0], &fs.ICAO24); err != nil {
		return nil, fmt.Errorf("state[0] icao24: %w", err)
	}

	// 1: callsign (trim trailing spaces)
	var cs string
	if err := json.Unmarshal(elems[1], &cs); err != nil {
		return nil, fmt.Errorf("state[1] callsign: %w", err)
	}
	fs.Callsign = strings.TrimRight(cs, " ")

	// 2: origin_country
	if err := json.Unmarshal(elems[2], &fs.Country); err != nil {
		return nil, fmt.Errorf("state[2] origin_country: %w", err)
	}

	// 3: time_position (nullable int)
	fs.TimePosition = nullInt64(elems[3])

	// 4: last_contact (int)
	var lc int64
	if err := json.Unmarshal(elems[4], &lc); err != nil {
		return nil, fmt.Errorf("state[4] last_contact: %w", err)
	}
	fs.LastContact = lc

	// 5: longitude (nullable float)
	fs.Longitude = nullFloat64(elems[5])

	// 6: latitude (nullable float)
	fs.Latitude = nullFloat64(elems[6])

	// 7: baro_altitude (nullable float)
	fs.BaroAltitude = nullFloat64(elems[7])

	// 8: on_ground (bool)
	if err := json.Unmarshal(elems[8], &fs.OnGround); err != nil {
		return nil, fmt.Errorf("state[8] on_ground: %w", err)
	}

	// 9: velocity (nullable float)
	fs.Velocity = nullFloat64(elems[9])

	// 10: true_track (nullable float)
	fs.TrueTrack = nullFloat64(elems[10])

	// 11: vertical_rate (nullable float)
	fs.VerticalRate = nullFloat64(elems[11])

	// 12: sensors (nullable array) — we ignore the value, just skip
	// 13: geo_altitude (nullable float)
	fs.GeoAltitude = nullFloat64(elems[13])

	// 14: squawk (nullable string)
	var squawk *string
	if string(elems[14]) != "null" {
		var s string
		if err := json.Unmarshal(elems[14], &s); err == nil {
			squawk = &s
		}
	}
	if squawk != nil {
		fs.Squawk = *squawk
	}

	// 15: spi (bool)
	if err := json.Unmarshal(elems[15], &fs.SPI); err != nil {
		return nil, fmt.Errorf("state[15] spi: %w", err)
	}

	// 16: position_source (int)
	if err := json.Unmarshal(elems[16], &fs.PositionSource); err != nil {
		return nil, fmt.Errorf("state[16] position_source: %w", err)
	}

	return fs, nil
}

// nullInt64 decodes a JSON token that may be null into *int64.
func nullInt64(raw json.RawMessage) *int64 {
	if string(raw) == "null" {
		return nil
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return &v
}

// nullFloat64 decodes a JSON token that may be null into *float64.
func nullFloat64(raw json.RawMessage) *float64 {
	if string(raw) == "null" {
		return nil
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return &v
}

// get performs one HTTP GET with pacing, User-Agent, optional Basic auth,
// and exponential backoff on 429/5xx.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	retries := c.cfg.Retries
	if retries < 0 {
		retries = 0
	}
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		body, retry, err := c.do(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("opensky: get %s: %w", rawURL, lastErr)
}

func (c *Client) do(ctx context.Context, rawURL string) (body []byte, retry bool, err error) {
	c.pace()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

// pace blocks until at least Rate has elapsed since the previous request.
// It is goroutine-safe.
func (c *Client) pace() {
	if c.cfg.Rate <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait := c.cfg.Rate - time.Since(c.lastReq); wait > 0 {
		time.Sleep(wait)
	}
	c.lastReq = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
