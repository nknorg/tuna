package geo

import (
	"log"
	"strings"
	"sync"
	"time"
)

type GeoProvider interface {
	GetLocation(ip string) (*Location, error)
	FileName() string
	DownloadUrl() string
	LastUpdate() time.Time
	NeedUpdate() bool
	MaybeUpdate() error
	Ready() bool
	SetReady(bool)
	SetFileName(string)
}

type Location struct {
	IP          string `json:"ip"`
	CountryCode string `json:"countryCode"`
	Country     string `json:"country"`
	City        string `json:"city"`
}

var emptyLocation = Location{}
var geoLock sync.Mutex

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
	Allow      []Location `json:"allow"`
	Disallow   []Location `json:"disallow"`
	providers  []GeoProvider
	dbPath     string
	downloadDB bool
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
	if f.NeedGeoInfo() {
		loc = f.GetLocation(ip)
	} else {
		loc = &Location{IP: ip}
	}

	return f.AllowLocation(loc), nil
}

func (f *IPFilter) GetLocation(ip string) *Location {
	for _, p := range f.providers {
		if p.Ready() {
			loc := getLocationFromProvider(ip, p)
			if !loc.Empty() {
				return &loc
			}
		}
	}
	return &Location{CountryCode: "UNKNOWN", IP: ip}
}

func (f *IPFilter) AllowLocation(loc *Location) bool {
	if loc.Empty() {
		return true
	}

	for _, l := range f.Disallow {
		if l.Match(loc) {
			log.Printf("%s from %s dropped", loc.IP, loc.CountryCode)
			return false
		}
	}

	empty := true
	for _, l := range f.Allow {
		if l.Match(loc) {
			log.Printf("%s from %s passed", loc.IP, loc.CountryCode)
			return true
		}
		if !l.Empty() {
			empty = false
		}
	}

	return empty
}

func (f *IPFilter) AddProvider(download bool, path string) {
	f.downloadDB = download
	f.dbPath = path
	if f.downloadDB {
		aws := NewAWSProvider(f.dbPath)
		gcp := NewGCPProvider(f.dbPath)
		mm := NewMaxMindProvider(f.dbPath)
		f.providers = []GeoProvider{aws, gcp, mm}
	}

	ip2c := NewIP2CProvider()
	f.providers = append(f.providers, ip2c)
}

func (f *IPFilter) UpdateDataFile(c chan struct{}) {
	for {
		select {
		case _, ok := <-c:
			if !ok {
				return
			}
		default:
			for _, p := range f.providers {
				if len(p.FileName()) == 0 {
					continue
				}
				err := p.MaybeUpdate()
				if err != nil {
					log.Print(err)
					continue
				}
			}
		}
		time.Sleep(1 * time.Hour)
	}
}

func getLocationFromProvider(ip string, p GeoProvider) Location {
	loc, err := p.GetLocation(ip)
	if err != nil {
		log.Println(err)
	}
	return *loc
}
