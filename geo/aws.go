package geo

import (
	"io/ioutil"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/nknorg/tuna/util"
)

const (
	AWSGeoUrl  = "https://ip-ranges.amazonaws.com/ip-ranges.json"
	AWSExpired = 7 * 24 * time.Hour
	AWSFile    = "aws-ip.json"
)

type AWSProvider struct {
	Info     *AWSGeoInfo
	fileName string
	url      string
	expire   time.Duration
	ready    bool
}

type AWSGeoInfo struct {
	SyncToken  string      `json:"syncToken"`
	CreateDate string      `json:"createDate"`
	Prefixes   []AWSIPInfo `json:"prefixes"`
}

type AWSIPInfo struct {
	IPPrefix           string     `json:"ip_prefix"`
	Region             string     `json:"region"`
	Service            string     `json:"service"`
	NetworkBorderGroup string     `json:"network_border_group"`
	Subnet             *net.IPNet `json:"-"`
}

var AWSRegionMapping = map[string]string{
	"us-east-1":      "US",
	"us-east-2":      "US",
	"us-west-1":      "US",
	"us-west-2":      "US",
	"af-south-1":     "ZA",
	"ap-east-1":      "HK",
	"ap-south-1":     "IN",
	"ap-northeast-1": "JP",
	"ap-northeast-2": "KR",
	"ap-northeast-3": "JP",
	"ap-southeast-1": "SG",
	"ap-southeast-2": "AU",
	"ca-central-1":   "CA",
	"eu-central-1":   "DE",
	"eu-west-1":      "IE",
	"eu-west-2":      "GB",
	"eu-west-3":      "FR",
	"eu-south-1":     "IT",
	"eu-north-1":     "SE",
	"me-south-1":     "BH",
	"sa-east-1":      "BR",
}

func NewAWSProvider(path string) *AWSProvider {
	return &AWSProvider{
		url:      AWSGeoUrl,
		fileName: filepath.Join(path, AWSFile),
		expire:   AWSExpired,
	}
}

func (p *AWSProvider) MaybeUpdate() error {
	geoLock.Lock()
	defer geoLock.Unlock()
	if !p.NeedUpdate() && p.Info != nil {
		return nil
	}
	if p.NeedUpdate() {
		log.Println("Updating AWS geo db")
		dir, filename := path.Split(p.fileName)
		tmpFile, err := ioutil.TempFile(dir, filename+"-*")
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
		if len(p.Info.Prefixes[idx].IPPrefix) == 0 {
			continue
		}
		_, subnet, err := net.ParseCIDR(p.Info.Prefixes[idx].IPPrefix)
		if err != nil {
			log.Print(err)
			continue
		}
		p.Info.Prefixes[idx].Subnet = subnet
	}
	p.ready = true
	return nil
}

func (p *AWSProvider) GetLocation(ip string) (*Location, error) {
	loc, err := p.getLocationFromAWS(ip)
	if err != nil {
		return &emptyLocation, err
	}
	return loc, nil
}

func (p *AWSProvider) getLocationFromAWS(ip string) (*Location, error) {
	loc := parseAWS(ip, p.Info)
	return loc, nil
}

func parseAWS(ip string, info *AWSGeoInfo) *Location {
	loc := Location{}
	parsed := net.ParseIP(ip)
	for _, p := range info.Prefixes {
		if p.Subnet == nil {
			continue
		}
		if p.Subnet.Contains(parsed) {
			if code, ok := AWSRegionMapping[p.Region]; ok {
				loc.CountryCode = code
				loc.IP = ip
				break
			}
		}
	}
	return &loc
}

func (p *AWSProvider) SetReady(ready bool) {
	p.ready = ready
}

func (p *AWSProvider) Ready() bool {
	return p.ready
}

func (p *AWSProvider) FileName() string {
	return p.fileName
}

func (p *AWSProvider) DownloadUrl() string {
	return p.url
}

func (p *AWSProvider) LastUpdate() time.Time {
	return getModTime(p.fileName)
}

func (p *AWSProvider) NeedUpdate() bool {
	return time.Since(p.LastUpdate()) > p.expire
}

func (p *AWSProvider) SetFileName(name string) {
	p.fileName = name
}
