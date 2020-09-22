package geo

import (
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/geoip2-golang"
)

const (
	Geolite2Url    = "https://raw.githubusercontent.com/leo108/geolite2-db/master/Country.mmdb"
	MaxMindExpired = 30 * 24 * time.Hour
	MaxMindFile    = "geolite2-country.mmdb"
)

type MaxMindProvider struct {
	DB       *geoip2.Reader
	fileName string
	url      string
	expire   time.Duration
	ready    bool
}

func (p *MaxMindProvider) GetLocation(ip string) (*Location, error) {
	loc, err := p.getLocationFromMM(ip)
	if err != nil {
		return &emptyLocation, err
	}
	return loc, nil
}

func NewMaxMindProvider(path string) *MaxMindProvider {
	p := filepath.Join(path, MaxMindFile)
	return &MaxMindProvider{url: MaxMindFile, fileName: p, expire: MaxMindExpired}
}

func (p *MaxMindProvider) MaybeUpdate() error {
	geoLock.Lock()
	defer geoLock.Unlock()
	if !p.NeedUpdate() && p.DB != nil {
		return nil
	}
	if p.NeedUpdate() {
		tmpFile, err := ioutil.TempFile("", p.fileName+"-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()

		client := http.Client{
			Timeout: 60 * time.Second,
		}
		resp, err := client.Get(Geolite2Url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		_, err = io.Copy(tmpFile, resp.Body)
		if err != nil {
			return err
		}
		err = os.Rename(tmpFile.Name(), p.fileName)
		if err != nil {
			return err
		}
	}

	db, err := geoip2.Open(p.fileName)
	if err != nil {
		os.Remove(p.fileName)
		return err
	}

	p.DB = db
	p.ready = true
	return nil
}

func (p *MaxMindProvider) getLocationFromMM(ip string) (*Location, error) {
	parsed := net.ParseIP(ip)
	record, err := p.DB.Country(parsed)
	if err != nil {
		return nil, err
	}
	return &Location{CountryCode: record.Country.IsoCode, IP: ip}, nil
}

func (p *MaxMindProvider) FileName() string {
	return p.fileName
}

func (p *MaxMindProvider) DownloadUrl() string {
	return p.url
}

func (p *MaxMindProvider) LastUpdate() time.Time {
	fs, err := os.Stat(p.fileName)
	if err != nil {
		log.Print(err)
		return time.Time{}
	}

	return fs.ModTime()
}

func (p *MaxMindProvider) NeedUpdate() bool {
	return time.Since(p.LastUpdate()) > p.expire
}

func (p *MaxMindProvider) SetReady(ready bool) {
	p.ready = ready
}

func (p *MaxMindProvider) Ready() bool {
	return p.ready
}

func (p *MaxMindProvider) SetFileName(name string) {
	p.fileName = name
}
