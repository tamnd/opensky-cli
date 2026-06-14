package opensky

import (
	"testing"

	"github.com/tamnd/any-cli/kit"
)

// These tests are offline: they exercise the URI driver's pure string functions
// and the host wiring (mint, body, resolve), which need no network. The
// client's HTTP behaviour is covered in opensky_test.go.

func TestDomainInfo(t *testing.T) {
	info := Domain{}.Info()
	if info.Scheme != "opensky" {
		t.Errorf("Scheme = %q, want opensky", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, Host)
	}
	if info.Identity.Binary != "opensky" {
		t.Errorf("Identity.Binary = %q, want opensky", info.Identity.Binary)
	}
	if info.Identity.Short == "" {
		t.Error("Identity.Short is empty")
	}
	if info.Identity.Repo == "" {
		t.Error("Identity.Repo is empty")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		in  string
		typ string
		id  string
	}{
		{"3c6444", "aircraft", "3c6444"},
		{"3C6444", "aircraft", "3c6444"}, // normalised to lower
		{"  a4d221  ", "aircraft", "a4d221"},
	}
	for _, tc := range cases {
		typ, id, err := Domain{}.Classify(tc.in)
		if err != nil {
			t.Errorf("Classify(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if typ != tc.typ {
			t.Errorf("Classify(%q) type = %q, want %q", tc.in, typ, tc.typ)
		}
		if id != tc.id {
			t.Errorf("Classify(%q) id = %q, want %q", tc.in, id, tc.id)
		}
	}
}

func TestClassifyEmpty(t *testing.T) {
	_, _, err := Domain{}.Classify("")
	if err == nil {
		t.Error("Classify(\"\") should return an error")
	}
}

func TestLocate(t *testing.T) {
	got, err := Domain{}.Locate("aircraft", "3c6444")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	want := DefaultBaseURL + "/states/all?icao24=3c6444"
	if got != want {
		t.Errorf("Locate = %q, want %q", got, want)
	}
}

func TestLocateUnknownType(t *testing.T) {
	_, err := Domain{}.Locate("page", "foo")
	if err == nil {
		t.Error("Locate with unknown type should return error")
	}
}

func TestParseBox(t *testing.T) {
	cases := []struct {
		in      string
		want    []float64
		wantErr bool
	}{
		{"45.8,5.9,47.8,10.5", []float64{45.8, 5.9, 47.8, 10.5}, false},
		{"0,0,90,180", []float64{0, 0, 90, 180}, false},
		{"-90,-180,90,180", []float64{-90, -180, 90, 180}, false},
		{"bad", nil, true},
		{"1,2,3", nil, true},
		{"1,2,3,a", nil, true},
	}
	for _, tc := range cases {
		got, err := parseBox(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseBox(%q) expected error, got nil", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBox(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if len(got) != 4 {
			t.Errorf("parseBox(%q) len = %d, want 4", tc.in, len(got))
			continue
		}
		for i, v := range tc.want {
			if got[i] != v {
				t.Errorf("parseBox(%q)[%d] = %v, want %v", tc.in, i, got[i], v)
			}
		}
	}
}

func TestHostWiring(t *testing.T) {
	h, err := kit.Open()
	if err != nil {
		t.Fatal(err)
	}

	fs := &FlightState{
		ICAO24:   "3c6444",
		Callsign: "DLH1234",
		Country:  "Germany",
	}
	u, err := h.Mint(fs)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if want := "opensky://aircraft/3c6444"; u.String() != want {
		t.Errorf("Mint = %q, want %q", u.String(), want)
	}

	got, err := h.ResolveOn("opensky", "a4d221")
	if err != nil {
		t.Fatalf("ResolveOn: %v", err)
	}
	if want := "opensky://aircraft/a4d221"; got.String() != want {
		t.Errorf("ResolveOn = %q, want %q", got.String(), want)
	}
}
