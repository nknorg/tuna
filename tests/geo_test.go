package tests

import (
	"testing"

	"github.com/nknorg/tuna"
)

type testCase struct {
	f        tuna.IPFilter
	location tuna.Location
	result   bool
}

var IP1 = "1.0.0.0"
var IP2 = "2.0.0.0"
var IP3 = "3.0.0.0"
var IP4 = "4.0.0.0"

var testData = []testCase{
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{},
			Disallow: []tuna.Location{{IP: IP1}},
		},
		location: tuna.Location{IP: IP3},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{},
			Disallow: []tuna.Location{{}},
		},
		location: tuna.Location{IP: IP3},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{{}},
			Disallow: []tuna.Location{{}},
		},
		location: tuna.Location{IP: IP3},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{IP: IP1}},
		},
		location: tuna.Location{IP: IP1},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{},
			Disallow: []tuna.Location{},
		},
		location: tuna.Location{IP: IP1},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{{IP: IP1}},
			Disallow: []tuna.Location{{IP: IP1}},
		},
		location: tuna.Location{IP: IP1},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{{IP: IP1}},
			Disallow: []tuna.Location{{IP: IP2}},
		},
		location: tuna.Location{},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{IP: IP3}},
		},
		location: tuna.Location{IP: IP4},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow: []tuna.Location{{IP: IP3}},
		},
		location: tuna.Location{IP: IP3},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{},
			Disallow: []tuna.Location{},
		},
		location: tuna.Location{},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{{IP: IP1}},
			Disallow: []tuna.Location{{IP: IP2}},
		},
		location: tuna.Location{IP: IP3},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Allow: []tuna.Location{{IP: IP1}, {IP: IP2}},
		},
		location: tuna.Location{IP: IP3},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Allow: []tuna.Location{{IP: IP1}, {}},
		},
		location: tuna.Location{IP: IP1},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow: []tuna.Location{{IP: IP1}, {}},
		},
		location: tuna.Location{IP: IP2},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{}, {IP: IP1}},
		},
		location: tuna.Location{IP: IP1},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{IP: IP1}, {}},
		},
		location: tuna.Location{IP: IP2},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow: []tuna.Location{{CountryCode: "US"}},
		},
		location: tuna.Location{CountryCode: "US"},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{CountryCode: "US"}},
		},
		location: tuna.Location{CountryCode: "US"},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{CountryCode: "US"}},
		},
		location: tuna.Location{CountryCode: "CA"},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow: []tuna.Location{{CountryCode: "US"}},
		},
		location: tuna.Location{CountryCode: "CA"},
		result:   false,
	},
	{
		f: tuna.IPFilter{
			Allow: []tuna.Location{{CountryCode: "US"}},
		},
		location: tuna.Location{},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{CountryCode: "US"}},
		},
		location: tuna.Location{},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Disallow: []tuna.Location{{CountryCode: "US"}},
		},
		location: tuna.Location{CountryCode: "UNKNOWN"},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{},
			Disallow: []tuna.Location{},
		},
		location: tuna.Location{CountryCode: "US"},
		result:   true,
	},
	{
		f: tuna.IPFilter{
			Allow:    []tuna.Location{},
			Disallow: []tuna.Location{},
		},
		location: tuna.Location{},
		result:   true,
	},
}

func TestLocationCheck(t *testing.T) {
	for num, data := range testData {
		res := data.f.ValidCheck(&data.location)
		if res != data.result {
			t.Fatalf("NO %d testcase failed", num+1)
		}
	}
}
