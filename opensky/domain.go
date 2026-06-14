package opensky

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes the OpenSky Network as a kit Domain. A multi-domain host
// (ant) enables the driver with a single blank import:
//
//	import _ "github.com/tamnd/opensky-cli/opensky"
//
// The init below registers it; the host dereferences opensky:// URIs by routing
// to the operations Register installs. The same Domain builds the standalone
// opensky binary (see cli/root.go), so the binary and a host share one source
// of truth.
func init() { kit.Register(Domain{}) }

// Domain is the OpenSky Network driver. It carries no state; the per-run
// client is built by the factory Register hands to kit.
type Domain struct{}

// Info describes the scheme, the hostnames a pasted link is matched against,
// and the identity reused for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "opensky",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "opensky",
			Short:  "real-time flight tracking via OpenSky Network API",
			Long: `real-time flight tracking via OpenSky Network API

opensky reads live flight data from the OpenSky Network over plain HTTPS,
shapes it into clean records, and prints output that pipes into the rest of
your tools. No API key required for anonymous access.`,
			Site: Host,
			Repo: "https://github.com/tamnd/opensky-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	// states: list current flight states with optional filters
	kit.Handle(app, kit.OpMeta{
		Name:    "states",
		Group:   "read",
		List:    true,
		Summary: "List current flight states, optionally filtered by bounding box or ICAO24",
		Args: []kit.Arg{
			{Name: "icao24", Help: "filter by ICAO24 transponder code (optional)"},
		},
	}, listStates)

	// aircraft: get current state for a specific aircraft
	kit.Handle(app, kit.OpMeta{
		Name:     "aircraft",
		Group:    "read",
		Single:   true,
		Summary:  "Get current state for a specific aircraft by ICAO24 code",
		URIType:  "aircraft",
		Resolver: true,
		Args:     []kit.Arg{{Name: "icao24", Help: "ICAO24 transponder code (e.g. 3c6444)"}},
	}, getAircraft)
}

// newClient builds a Client from the kit-resolved config, so the standalone
// binary and a host pace and identify themselves the same way.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	c := DefaultConfig()
	if cfg.UserAgent != "" {
		c.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		c.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		c.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		c.Timeout = cfg.Timeout
	}
	return NewClientWithConfig(c), nil
}

// --- input structs ---

type statesIn struct {
	ICAO24 string  `kit:"arg,optional" help:"filter by ICAO24 transponder code"`
	Box    string  `kit:"flag" help:"bounding box as minLat,minLon,maxLat,maxLon"`
	Limit  int     `kit:"flag,inherit" help:"max results"`
	Client *Client `kit:"inject"`
}

type aircraftIn struct {
	ICAO24 string  `kit:"arg" help:"ICAO24 transponder code (e.g. 3c6444)"`
	Client *Client `kit:"inject"`
}

// --- handlers ---

func listStates(ctx context.Context, in statesIn, emit func(*FlightState) error) error {
	f := StatesFilter{
		ICAO24: in.ICAO24,
		Limit:  in.Limit,
	}

	if in.Box != "" {
		bbox, err := parseBox(in.Box)
		if err != nil {
			return errs.Usage("--box: %v", err)
		}
		f.BBox = bbox
	}

	states, err := in.Client.States(ctx, f)
	if err != nil {
		return mapErr(err)
	}
	for _, s := range states {
		if err := emit(s); err != nil {
			return err
		}
	}
	return nil
}

func getAircraft(ctx context.Context, in aircraftIn, emit func(*FlightState) error) error {
	f := StatesFilter{
		ICAO24: in.ICAO24,
		Limit:  1,
	}
	states, err := in.Client.States(ctx, f)
	if err != nil {
		return mapErr(err)
	}
	if len(states) == 0 {
		return errs.NotFound("aircraft %q not found in current states", in.ICAO24)
	}
	return emit(states[0])
}

// --- Resolver: URI string functions, pure and network-free ---

// Classify turns an ICAO24 code or an opensky-network.org URL into
// (type="aircraft", id=icao24).
func (Domain) Classify(input string) (uriType, id string, err error) {
	id = strings.ToLower(strings.TrimSpace(input))
	if id == "" {
		return "", "", errs.Usage("unrecognized opensky reference: %q", input)
	}
	return "aircraft", id, nil
}

// Locate returns the canonical API URL for a given (type, id).
func (Domain) Locate(uriType, id string) (string, error) {
	switch uriType {
	case "aircraft":
		return DefaultBaseURL + "/states/all?icao24=" + strings.ToLower(id), nil
	default:
		return "", errs.Usage("opensky has no resource type %q", uriType)
	}
}

// --- helpers ---

// parseBox parses "minLat,minLon,maxLat,maxLon" into a []float64 of length 4.
func parseBox(s string) ([]float64, error) {
	parts := strings.SplitN(s, ",", 5)
	if len(parts) != 4 {
		return nil, fmt.Errorf("want 4 comma-separated values, got %d", len(parts))
	}
	out := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("value %d %q: %w", i+1, p, err)
		}
		out[i] = v
	}
	return out, nil
}

// mapErr converts library errors to kit error kinds.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "http 429") {
		return errs.RateLimited("%s", err.Error())
	}
	if strings.Contains(err.Error(), "http 404") {
		return errs.NotFound("%s", err.Error())
	}
	return err
}

// positionSourceName returns a human-readable name for the position source int.
func positionSourceName(src int) string {
	switch src {
	case 0:
		return "ADS-B"
	case 1:
		return "ASTERIX"
	case 2:
		return "MLAT"
	case 3:
		return "FLARM"
	default:
		return strconv.Itoa(src)
	}
}

// lastContactAge returns a human-readable age string for a Unix timestamp.
func lastContactAge(ts int64) string {
	d := time.Since(time.Unix(ts, 0)).Truncate(time.Second)
	return d.String() + " ago"
}

// Silence unused-variable warnings for helper functions that are referenced
// only in output formatting outside of tests.
var _ = positionSourceName
var _ = lastContactAge
