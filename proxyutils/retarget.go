package proxyutils

import (
	"net/http"
	"net/url"
)

// RetargetMap is a map that contains utility functions for retargeting HTTP
// headers from one server to another
// -> key is the host without the scheme
type RetargetMap map[string]*url.URL

// Adds a new target mapping (and it's reverse mapping)
func (this RetargetMap) Add(source *url.URL, target *url.URL) {
	this[source.Host] = target
	this[target.Host] = source
}

// Retargets a header that is only a URL (Location, Referer, etc)
func (this RetargetMap) Retarget(header *http.Header, name string, defaultUrl *url.URL) {
	origStr := header.Get(name)
	if origStr != "" {
		hUrl, _ := url.Parse(origStr)
		if hUrl != nil {
			// look up the redirect in our map
			target := this[hUrl.Host]
			if target == nil {
				target = defaultUrl
			}

			hUrl.Scheme = target.Scheme
			hUrl.Host = target.Host
			header.Set(name, hUrl.String())
		}
	}
}
