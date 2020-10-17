package geo

import (
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/nknorg/tuna/util"
)

const (
	GCPGeoUrl  = "https://www.gstatic.com/ipranges/cloud.json"
	GCPExpired = 7 * 24 * time.Hour
	GCPFile    = "gcp-ip.json"
)

type GCPProvider struct {
	Info     *GCPGeoInfo
	fileName string
	url      string
	expire   time.Duration
	ready    bool
}

type GCPGeoInfo struct {
	SyncToken    string      `json:"syncToken"`
	CreationTime string      `json:"creationTime"`
	Prefixes     []GCPIPInfo `json:"prefixes"`
}

type GCPIPInfo struct {
	Ipv4Prefix string     `json:"ipv4Prefix"`
	Service    string     `json:"service"`
	Scope      string     `json:"scope"`
	Subnet     *net.IPNet `json:"-"`
}

var GCPScopeMapping = map[string]string{
	"asia-east1":              "TW",
	"asia-east2":              "HK",
	"asia-northeast1":         "JP",
	"asia-northeast2":         "JP",
	"asia-northeast3":         "KR",
	"asia-south1":             "IN",
	"asia-southeast1":         "SG",
	"asia-southeast2":         "ID",
	"australia-southeast1":    "AU",
	"europe-north1":           "FI",
	"europe-west1":            "BE",
	"europe-west2":            "GB",
	"europe-west3":            "DE",
	"europe-west4":            "NL",
	"europe-west6":            "CH",
	"northamerica-northeast1": "CA",
	"southamerica-east1":      "BR",
	"us-central1":             "US",
	"us-east1":                "US",
	"us-east4":                "US",
	"us-west1":                "US",
	"us-west2":                "US",
	"us-west3":                "US",
	"us-west4":                "US",
}

func NewGCPProvider(path string) *GCPProvider {
	p := filepath.Join(path, GCPFile)
	return &GCPProvider{url: GCPGeoUrl, fileName: p, expire: GCPExpired}
}

func (p *GCPProvider) MaybeUpdate() error {
	geoLock.Lock()
	defer geoLock.Unlock()
	if !p.NeedUpdate() && p.Info != nil {
		return nil
	}
	if p.NeedUpdate() {
		log.Println("Updating GCP geo db")
		tmpFile, err := ioutil.TempFile("", p.fileName+"-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()
		err = util.DownloadJsonFile(p.url, tmpFile.Name())
		if err != nil {
			return err
		}
		err = os.Rename(tmpFile.Name(), p.fileName)
		if err != nil {
			return err
		}
	}
	err := util.ReadJSON(p.fileName, &p.Info)
	if err != nil {
		return err
	}
	for idx := range p.Info.Prefixes {
		if len(p.Info.Prefixes[idx].Ipv4Prefix) == 0 {
			continue
		}
		_, subnet, err := net.ParseCIDR(p.Info.Prefixes[idx].Ipv4Prefix)
		if err != nil {
			log.Print(err)
			continue
		}
		p.Info.Prefixes[idx].Subnet = subnet
	}
	p.ready = true
	return nil
}

func (p *GCPProvider) GetLocation(ip string) (*Location, error) {
	loc, err := p.getLocationFromGCP(ip)
	if err != nil {
		return &emptyLocation, err
	}
	return loc, nil
}

func (p *GCPProvider) getLocationFromGCP(ip string) (*Location, error) {
	loc := parseGCP(ip, p.Info)
	return loc, nil
}

func parseGCP(ip string, info *GCPGeoInfo) *Location {
	loc := Location{}
	parsed := net.ParseIP(ip)
	for _, p := range info.Prefixes {
		if p.Subnet == nil {
			continue
		}
		if p.Subnet.Contains(parsed) {
			if code, ok := GCPScopeMapping[p.Scope]; ok {
				loc.CountryCode = code
				loc.IP = ip
				break
			}
		}
	}
	return &loc
}

func (p *GCPProvider) SetReady(ready bool) {
	p.ready = ready
}

func (p *GCPProvider) Ready() bool {
	return p.ready
}

func (p *GCPProvider) FileName() string {
	return p.fileName
}

func (p *GCPProvider) DownloadUrl() string {
	return p.url
}

func (p *GCPProvider) LastUpdate() time.Time {
	return getModTime(p.fileName)
}

func (p *GCPProvider) NeedUpdate() bool {
	return time.Since(p.LastUpdate()) > p.expire
}

func (p *GCPProvider) SetFileName(name string) {
	p.fileName = name
}
