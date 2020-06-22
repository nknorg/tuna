package tuna

import (
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	GeoIPRetry = 3
)

type Location struct {
	IP          string `json:"ip"`
	CountryCode string `json:"countryCode"`
	Country     string `json:"country"`
	City        string `json:"city"`
}

var emptyLocation = Location{}

func (l *Location) Empty() bool {
	if l == nil {
		return true
	}
	return *l == emptyLocation
}

func (l *Location) Match(location *Location) bool {
	if len(l.IP) > 0 && location.IP == l.IP {
		return true
	}
	if len(l.CountryCode) > 0 && strings.ToLower(location.CountryCode) == strings.ToLower(l.CountryCode) {
		return true
	}
	return false
}

type IPFilter struct {
	Allow    []Location `json:"allow"`
	Disallow []Location `json:"disallow"`
}

func (f *IPFilter) Empty() bool {
	if f == nil {
		return true
	}
	for _, loc := range f.Allow {
		if !loc.Empty() {
			return false
		}
	}
	for _, loc := range f.Disallow {
		if !loc.Empty() {
			return false
		}
	}
	return true
}

func (f *IPFilter) NeedGeoInfo() bool {
	if f.Empty() {
		return false
	}
	for _, loc := range f.Allow {
		if len(loc.CountryCode) > 0 || len(loc.Country) > 0 || len(loc.City) > 0 {
			return true
		}
	}
	for _, loc := range f.Disallow {
		if len(loc.CountryCode) > 0 || len(loc.Country) > 0 || len(loc.City) > 0 {
			return true
		}
	}
	return false
}

func (f *IPFilter) AllowIP(ip string) (bool, error) {
	if f.Empty() {
		return true, nil
	}

	var loc *Location
	var err error
	if f.NeedGeoInfo() {
		loc, err = getLocationFromIP2C(ip, GeoIPRetry)
		if err != nil {
			return true, err
		}
	} else {
		loc = &Location{IP: ip}
	}

	return f.AllowLocation(loc), nil
}

func (f *IPFilter) AllowLocation(loc *Location) bool {
	if loc.Empty() {
		return true
	}

	for _, l := range f.Disallow {
		if l.Match(loc) {
			return false
		}
	}

	empty := true
	for _, l := range f.Allow {
		if l.Match(loc) {
			return true
		}
		if !l.Empty() {
			empty = false
		}
	}

	return empty
}

func getLocationFromIP2C(ip string, retry int) (*Location, error) {
	queryURL := "https://ip2c.org/" + ip
	client := http.Client{
		Timeout: 10 * time.Second,
	}

	i := 0
	var resp *http.Response
	var err error
	for ; i < retry; i++ {
		resp, err = client.Get(queryURL)
		if err != nil {
			log.Println(err)
			continue
		}
		break
	}
	if i == retry {
		return &emptyLocation, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return &emptyLocation, err
	}

	loc, err := parseIP2C(string(body))
	if err != nil {
		log.Println(err)
		return &Location{CountryCode: "UNKNOWN"}, nil
	}

	loc.IP = ip

	return loc, nil
}

func parseIP2C(body string) (*Location, error) {
	if len(body) == 0 {
		return nil, nil
	}

	if body[0] != byte('1') {
		return nil, errors.New("get ip2c result err")
	}

	res := strings.Split(body, ";")
	if len(res) != 4 {
		return nil, errors.New("invalid response from ip2c service")
	}

	l := &Location{}
	l.CountryCode = res[1]
	l.Country = res[3]

	return l, nil
}
