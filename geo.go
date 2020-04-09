package tuna

import (
	"errors"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

const IP2CUrl = "https://ip2c.org/"

type Location struct {
	IP          string `json:"IP"`
	CountryCode string `json:"CountryCode"`
	Country     string `json:"Country"`
	City        string `json:"City"`
}

var emptyLocation = Location{}

func (l *Location) Empty() bool {
	if l == nil {
		return true
	}
	return *l == emptyLocation
}

func (l *Location) Match(location *Location) bool {
	if len(location.IP) > 0 && location.IP == l.IP {
		return true
	}
	if len(location.CountryCode) > 0 && location.CountryCode == l.CountryCode {
		return true
	}
	return false
}

type IPFilter struct {
	Allow    []Location `json:"Allow"`
	Disallow []Location `json:"Disallow"`
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

func getLocationFromIP2C(ip string) (*Location, error) {
	queryUrl := IP2CUrl + ip
	client := http.Client{
		Timeout: 10 * time.Second,
	}
	unknown := &Location{CountryCode: "UNKNOWN"}
	resp, err := client.Get(queryUrl)
	if err != nil {
		return unknown, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return unknown, err
	}
	loc, err := parseIP2C(string(body))
	if err != nil {
		return unknown, err
	}
	loc.IP = ip
	return loc, nil
}

func parseIP2C(body string) (*Location, error) {
	if body[0:1] != "1" {
		return nil, errors.New("get ip2c result err")
	}
	res := strings.Split(body, ";")
	if len(res) != 4 {
		return nil, errors.New("invalid response from ip2c service")
	}

	l := new(Location)
	l.CountryCode = res[1]
	l.Country = res[3]
	return l, nil
}

func geoCheck(f *IPFilter, ip string) (bool, error) {
	if f.Empty() {
		return true, nil
	}
	loc, err := getLocationFromIP2C(ip)
	if err != nil {
		return false, err
	}

	valid := ValidCheck(f, loc)
	return valid, nil
}

func ValidCheck(f *IPFilter, loc *Location) bool {
	if len(f.Allow) > 0 {
		for _, l := range f.Allow {
			if l.Match(loc) {
				return true
			}
		}
		return false
	}
	for _, l := range f.Disallow {
		if l.Match(loc) {
			return false
		}
	}
	return true
}
