package geo

import (
	"context"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/nknorg/tuna/util"

	"github.com/oschwald/geoip2-golang"
)

const (
	Geolite2Url    = "https://gitlab.com/leo108/geolite2-db/-/raw/master/Country.mmdb"
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
	return &MaxMindProvider{
		url:      MaxMindFile,
		fileName: filepath.Join(path, MaxMindFile),
		expire:   MaxMindExpired,
	}
}

func (p *MaxMindProvider) MaybeUpdate() error {
	return p.MaybeUpdateContext(context.Background())
}

func (p *MaxMindProvider) MaybeUpdateContext(ctx context.Context) error {
	geoLock.Lock()
	defer geoLock.Unlock()
	if !p.NeedUpdate() && p.DB != nil {
		return nil
	}
	if p.NeedUpdate() {
		log.Println("Updating geolite db")
		tmpFile, err := ioutil.TempFile(path.Dir(p.fileName), path.Base(p.fileName)+"-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()

		req, err := http.NewRequestWithContext(ctx, "GET", Geolite2Url, nil)
		if err != nil {
			return err
		}
		client := http.Client{
			Timeout: 300 * time.Second,
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		_, err = io.Copy(tmpFile, resp.Body)
		if err != nil {
			return err
		}
		err = util.CopyFile(tmpFile.Name(), p.fileName)
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
	return getModTime(p.fileName)
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
