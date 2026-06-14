package opensky_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tamnd/opensky-cli/opensky"
)

// mockState builds a raw JSON array in the OpenSky state-vector format.
// All 17 elements must be present; null is the zero value for nullable fields.
var mockStates = [][]interface{}{
	// 0:icao24, 1:callsign, 2:country, 3:time_position, 4:last_contact,
	// 5:lon, 6:lat, 7:baro_alt, 8:on_ground, 9:velocity,
	// 10:true_track, 11:vert_rate, 12:sensors, 13:geo_alt,
	// 14:squawk, 15:spi, 16:position_source
	{"3c6444", "DLH1234 ", "Germany", nil, 1617235200, 8.12, 48.23, 9000.0, false, 220.5, 270.0, 0.5, nil, 8800.0, "1234", false, 0},
	{"4b1902", "SWR456  ", "Switzerland", 1617235100, 1617235200, 7.56, 47.38, 11000.0, false, 250.0, 90.0, -1.0, nil, 10800.0, "5678", false, 0},
	{"406a50", "BAW789  ", "United Kingdom", nil, 1617235150, 6.88, 46.91, nil, true, nil, nil, nil, nil, nil, nil, false, 2},
}

func statesPayload(states [][]interface{}) []byte {
	raws := make([]json.RawMessage, len(states))
	for i, s := range states {
		b, _ := json.Marshal(s)
		raws[i] = json.RawMessage(b)
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"time":   1617235200,
		"states": raws,
	})
	return payload
}

func newTestServer(handler http.HandlerFunc) (*httptest.Server, *opensky.Client) {
	ts := httptest.NewServer(handler)
	cfg := opensky.DefaultConfig()
	cfg.BaseURL = ts.URL
	cfg.Rate = 0
	c := opensky.NewClientWithConfig(cfg)
	return ts, c
}

// TestStatesAll verifies that the client fetches and parses all states when no
// filter is applied, and that field values are decoded correctly.
func TestStatesAll(t *testing.T) {
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/states/all" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Error("missing User-Agent header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(statesPayload(mockStates))
	})
	defer ts.Close()

	states, err := c.States(context.Background(), opensky.StatesFilter{})
	if err != nil {
		t.Fatalf("States() error: %v", err)
	}
	if len(states) != 3 {
		t.Fatalf("got %d states, want 3", len(states))
	}

	s0 := states[0]
	if s0.ICAO24 != "3c6444" {
		t.Errorf("ICAO24 = %q, want 3c6444", s0.ICAO24)
	}
	if s0.Callsign != "DLH1234" {
		t.Errorf("Callsign = %q, want DLH1234 (trailing spaces trimmed)", s0.Callsign)
	}
	if s0.Country != "Germany" {
		t.Errorf("Country = %q, want Germany", s0.Country)
	}
	if s0.TimePosition != nil {
		t.Errorf("TimePosition = %v, want nil (was null in JSON)", s0.TimePosition)
	}
	if s0.LastContact != 1617235200 {
		t.Errorf("LastContact = %d, want 1617235200", s0.LastContact)
	}
	if s0.Longitude == nil || *s0.Longitude != 8.12 {
		t.Errorf("Longitude = %v, want 8.12", s0.Longitude)
	}
	if s0.BaroAltitude == nil || *s0.BaroAltitude != 9000.0 {
		t.Errorf("BaroAltitude = %v, want 9000.0", s0.BaroAltitude)
	}
	if s0.OnGround {
		t.Error("OnGround = true, want false")
	}
	if s0.Squawk != "1234" {
		t.Errorf("Squawk = %q, want 1234", s0.Squawk)
	}
	if s0.PositionSource != 0 {
		t.Errorf("PositionSource = %d, want 0 (ADS-B)", s0.PositionSource)
	}
}

// TestStatesLimit verifies that the Limit filter caps the number of results.
func TestStatesLimit(t *testing.T) {
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(statesPayload(mockStates))
	})
	defer ts.Close()

	states, err := c.States(context.Background(), opensky.StatesFilter{Limit: 2})
	if err != nil {
		t.Fatalf("States() error: %v", err)
	}
	if len(states) != 2 {
		t.Errorf("got %d states with Limit=2, want 2", len(states))
	}
}

// TestStatesBBox verifies that bounding-box parameters are sent as query params.
func TestStatesBBox(t *testing.T) {
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for _, key := range []string{"lamin", "lomin", "lamax", "lomax"} {
			if q.Get(key) == "" {
				t.Errorf("missing query param %q", key)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(statesPayload(mockStates[:2]))
	})
	defer ts.Close()

	f := opensky.StatesFilter{BBox: []float64{45.8, 5.9, 47.8, 10.5}}
	states, err := c.States(context.Background(), f)
	if err != nil {
		t.Fatalf("States() error: %v", err)
	}
	if len(states) != 2 {
		t.Errorf("got %d states, want 2", len(states))
	}
}

// TestStatesICAO24 verifies that the icao24 filter param is forwarded.
func TestStatesICAO24(t *testing.T) {
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("icao24"); got != "3c6444" {
			t.Errorf("icao24 param = %q, want 3c6444", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(statesPayload(mockStates[:1]))
	})
	defer ts.Close()

	f := opensky.StatesFilter{ICAO24: "3c6444"}
	states, err := c.States(context.Background(), f)
	if err != nil {
		t.Fatalf("States() error: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("got %d states, want 1", len(states))
	}
	if states[0].ICAO24 != "3c6444" {
		t.Errorf("ICAO24 = %q, want 3c6444", states[0].ICAO24)
	}
}

// TestStatesNullFields verifies that nullable fields are decoded as nil when
// the API sends JSON null, and that the on-ground state (no altitude/speed)
// decodes correctly.
func TestStatesNullFields(t *testing.T) {
	ts, c := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(statesPayload(mockStates))
	})
	defer ts.Close()

	states, err := c.States(context.Background(), opensky.StatesFilter{})
	if err != nil {
		t.Fatalf("States() error: %v", err)
	}
	if len(states) < 3 {
		t.Fatalf("need at least 3 states, got %d", len(states))
	}

	// states[2] is on the ground with many null fields
	s2 := states[2]
	if s2.ICAO24 != "406a50" {
		t.Fatalf("wrong aircraft, ICAO24 = %q", s2.ICAO24)
	}
	if !s2.OnGround {
		t.Error("OnGround = false, want true")
	}
	if s2.BaroAltitude != nil {
		t.Errorf("BaroAltitude = %v, want nil", s2.BaroAltitude)
	}
	if s2.Velocity != nil {
		t.Errorf("Velocity = %v, want nil", s2.Velocity)
	}
	if s2.Squawk != "" {
		t.Errorf("Squawk = %q, want empty (null)", s2.Squawk)
	}
	if s2.PositionSource != 2 {
		t.Errorf("PositionSource = %d, want 2 (MLAT)", s2.PositionSource)
	}
}

// TestRetryOn503 verifies that transient server errors are retried.
func TestRetryOn503(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(statesPayload(mockStates[:1]))
	}))
	defer ts.Close()

	cfg := opensky.DefaultConfig()
	cfg.BaseURL = ts.URL
	cfg.Rate = 0
	cfg.Retries = 5
	c := opensky.NewClientWithConfig(cfg)

	states, err := c.States(context.Background(), opensky.StatesFilter{})
	if err != nil {
		t.Fatalf("States() error after retries: %v", err)
	}
	if len(states) != 1 {
		t.Errorf("got %d states, want 1", len(states))
	}
	if hits != 3 {
		t.Errorf("server received %d requests, want 3", hits)
	}
}
