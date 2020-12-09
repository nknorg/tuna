package filter

import (
	"log"
)

type NknClient struct {
	Address string `json:"address"`
}

var emptyNknClient = NknClient{}

func (c *NknClient) Empty() bool {
	if c == nil {
		return true
	}
	return *c == emptyNknClient
}

func (c *NknClient) Match(nknClient *NknClient) bool {
	if len(c.Address) > 0 {
		if nknClient.Address == c.Address {
			return true
		}
		return false
	}
	return false
}

type NknFilter struct {
	Allow    []NknClient `json:"allow"`
	Disallow []NknClient `json:"disallow"`
}

func (f *NknFilter) Empty() bool {
	if f == nil {
		return true
	}
	for _, a := range f.Allow {
		if !a.Empty() {
			return false
		}
	}
	for _, d := range f.Disallow {
		if !d.Empty() {
			return false
		}
	}
	return true
}

func (f *NknFilter) IsAllow(nknClient *NknClient) bool {
	if f == nil {
		return true
	}
	if nknClient.Empty() {
		return true
	}

	for _, d := range f.Disallow {
		if d.Match(nknClient) {
			log.Printf("%s dropped", nknClient.Address)
			return false
		}
	}

	empty := true
	for _, a := range f.Allow {
		if a.Match(nknClient) {
			log.Printf("%s passed", nknClient.Address)
			return true
		}
		if !a.Empty() {
			empty = false
		}
	}

	return empty
}
