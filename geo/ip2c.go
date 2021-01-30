package geo

import (
	"context"
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	GeoIPRetry = 3
	IP2CUrl    = "http://ip2c.org/"
)

type IP2CProvider struct {
}

func NewIP2CProvider() *IP2CProvider {
	return new(IP2CProvider)
}

func (p *IP2CProvider) MaybeUpdate() error {
	return p.MaybeUpdateContext(context.Background())
}

func (p *IP2CProvider) MaybeUpdateContext(ctx context.Context) error {
	return nil
}

func (p *IP2CProvider) GetLocation(ip string) (*Location, error) {
	loc, err := p.getLocationFromIP2C(ip, GeoIPRetry)
	if err != nil {
		return &emptyLocation, err
	}
	return loc, nil
}

func (p *IP2CProvider) getLocationFromIP2C(ip string, retry int) (*Location, error) {
	queryURL := IP2CUrl + ip
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
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	loc, err := parseIP2C(ip, string(body))
	if err != nil {
		return nil, err
	}

	return loc, nil
}

func parseIP2C(ip, body string) (*Location, error) {
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
	l.IP = ip
	return l, nil
}

func (p *IP2CProvider) FileName() string {
	return ""
}

func (p *IP2CProvider) DownloadUrl() string {
	return ""
}

func (p *IP2CProvider) LastUpdate() time.Time {
	return time.Time{}
}

func (p *IP2CProvider) NeedUpdate() bool {
	return false
}

func (p *IP2CProvider) Ready() bool {
	return true
}

func (p *IP2CProvider) SetReady(bool) {}

func (p *IP2CProvider) SetFileName(string) {}
