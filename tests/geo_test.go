package tests

import (
	"testing"

	"github.com/nknorg/tuna/geo"
)

type testCase struct {
	f        geo.IPFilter
	location geo.Location
	result   bool
}

type testGeoCase struct {
	IP      string
	Country string
}

var IP1 = "1.0.0.0"
var IP2 = "2.0.0.0"
var IP3 = "3.0.0.0"
var IP4 = "4.0.0.0"

var testData = []testCase{
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{},
			Disallow: []geo.Location{{IP: IP1}},
		},
		location: geo.Location{IP: IP3},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{},
			Disallow: []geo.Location{{}},
		},
		location: geo.Location{IP: IP3},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{{}},
			Disallow: []geo.Location{{}},
		},
		location: geo.Location{IP: IP3},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{IP: IP1}},
		},
		location: geo.Location{IP: IP1},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{},
			Disallow: []geo.Location{},
		},
		location: geo.Location{IP: IP1},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{{IP: IP1}},
			Disallow: []geo.Location{{IP: IP1}},
		},
		location: geo.Location{IP: IP1},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{{IP: IP1}},
			Disallow: []geo.Location{{IP: IP2}},
		},
		location: geo.Location{},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{IP: IP3}},
		},
		location: geo.Location{IP: IP4},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow: []geo.Location{{IP: IP3}},
		},
		location: geo.Location{IP: IP3},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{},
			Disallow: []geo.Location{},
		},
		location: geo.Location{},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{{IP: IP1}},
			Disallow: []geo.Location{{IP: IP2}},
		},
		location: geo.Location{IP: IP3},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Allow: []geo.Location{{IP: IP1}, {IP: IP2}},
		},
		location: geo.Location{IP: IP3},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Allow: []geo.Location{{IP: IP1}, {}},
		},
		location: geo.Location{IP: IP1},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow: []geo.Location{{IP: IP1}, {}},
		},
		location: geo.Location{IP: IP2},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{}, {IP: IP1}},
		},
		location: geo.Location{IP: IP1},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{IP: IP1}, {}},
		},
		location: geo.Location{IP: IP2},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow: []geo.Location{{CountryCode: "US"}},
		},
		location: geo.Location{CountryCode: "US"},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{CountryCode: "US"}},
		},
		location: geo.Location{CountryCode: "US"},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{CountryCode: "US"}},
		},
		location: geo.Location{CountryCode: "CA"},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow: []geo.Location{{CountryCode: "US"}},
		},
		location: geo.Location{CountryCode: "CA"},
		result:   false,
	},
	{
		f: geo.IPFilter{
			Allow: []geo.Location{{CountryCode: "US"}},
		},
		location: geo.Location{},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{CountryCode: "US"}},
		},
		location: geo.Location{},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Disallow: []geo.Location{{CountryCode: "US"}},
		},
		location: geo.Location{CountryCode: "UNKNOWN"},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{},
			Disallow: []geo.Location{},
		},
		location: geo.Location{CountryCode: "US"},
		result:   true,
	},
	{
		f: geo.IPFilter{
			Allow:    []geo.Location{},
			Disallow: []geo.Location{},
		},
		location: geo.Location{},
		result:   true,
	},
}

var testGeoData = []testGeoCase{
	{
		IP:      "34.68.157.1", // Google Cloud
		Country: "US",
	},
	{
		IP:      "34.68.152.156", // Google Cloud
		Country: "US",
	},
	{
		IP:      "52.12.134.239", // AWS
		Country: "US",
	},
	{
		IP:      "172.104.210.83", // Linode
		Country: "US",
	},
	{
		IP:      "207.246.120.132", // Vultr
		Country: "US",
	},
	{
		IP:      "52.194.247.186", // AWS
		Country: "JP",
	},
	{
		IP:      "144.91.100.54", // Contabo
		Country: "DE",
	},
	{
		IP:      "3.9.118.165", // AWS
		Country: "GB",
	},
	{
		IP:      "161.35.59.158", // DO
		Country: "US",
	},
}

func TestLocationCheck(t *testing.T) {
	for num, data := range testData {
		res := data.f.AllowLocation(&data.location)
		if res != data.result {
			t.Fatalf("NO %d testcase failed", num+1)
		}
	}
}

func TestGetLocations(t *testing.T) {
	filter := &geo.IPFilter{}
	filter.AddProvider(true, ".")
	c := make(chan struct{})
	go filter.UpdateDataFile(c)
	for _, data := range testGeoData {
		loc := filter.GetLocation(data.IP)
		if loc.CountryCode != data.Country {
			t.Fatal(data)
		}
	}
	close(c)
}
