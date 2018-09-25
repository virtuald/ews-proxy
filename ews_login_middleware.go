package ews

import (
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/virtuald/ews-proxy/proxyutils"
)

// this middleware needs to be first in the chain
type LoginMiddleware struct {
	Translator *TranslationMiddleware
	Redirector *proxyutils.RedirectorMiddleware

	// string contains on path; typically /owa/
	CheckPath string

	// used for ews client
	Transport http.RoundTripper

	// disabled if 0
	KeepAlivePeriod time.Duration
	keepAliveTicker *time.Ticker

	CanaryFinder func(*http.Response) (string, error)
}

func (this *LoginMiddleware) RequestModifier(request *http.Request, cctx proxyutils.ChainContext) error {
	// special redirect -- tell the user to close the page
	if request.URL.Path == "/proxyclose.html" {
		response := proxyutils.CreateNewResponse(request, closePageHtml)
		return proxyutils.NewRequestError(response)
	}

	// store this in the context because other people modify it
	cctx["login_ctx"] = request.URL.Path
	return nil
}

// This processes /owa/ pages and searches for a valid OWA canary in the
// page cookies. Once the canary has been found, then it redirects to the
// /close page
func (this *LoginMiddleware) ResponseModifier(response *http.Response, cctx proxyutils.ChainContext) error {
	// Watch for OWA Canary info, and snag it
	if strings.Contains(cctx["login_ctx"].(string), this.CheckPath) && response.StatusCode != 302 {
		canary, err := this.CanaryFinder(response)
		if err != nil {
			return err
		} else if canary != "" {
			// if the user agent isn't set, set it since this access is being
			// done by a user's browser
			if this.Redirector.UserAgent == "" {
				this.Redirector.UserAgent = response.Header.Get("User-Agent")
			}

			// validate and set the canary if it's valid
			this.CheckLogin(canary)
		}

		// If we have a canary stored, _always_ tell the user's page to close, otherwise
		// eventually they'll make it to the OWA page
		if this.Translator.OwaCanary != "" {
			this.Translator.onSuccess()

			response.Body = ioutil.NopCloser(strings.NewReader(""))
			response.ContentLength = 0

			response.Header = http.Header{}
			response.Header.Set("Location", "/proxyclose.html")
			response.StatusCode = http.StatusFound
		}
	}

	return nil
}

func (this *LoginMiddleware) CookieCanaryFinder(response *http.Response) (string, error) {
	for _, cookie := range this.Redirector.Cookies.Cookies(this.Redirector.TargetServer) {
		if cookie.Name == "X-OWA-CANARY" {
			return cookie.Value, nil
		}
	}

	return "", nil
}

// CheckLogin returns false if login is required, and will
// invalidate the canary if the server responds that it is invalid
func (this *LoginMiddleware) CheckLogin(canary string) bool {

	if canary == "" {
		return false
	}

	client := http.Client{Transport: this.Transport}
	client.Jar = this.Redirector.Cookies

	req, err := http.NewRequest("POST", this.Redirector.TargetServer.ResolveReference(&url.URL{Path: this.Translator.OwaServicePath}).String(), nil)
	if err != nil {
		log.Printf("Error checking OWA: %s", err)
		this.Translator.OwaCanary = ""
		return false
	}

	SetupOwaRequest(this.Translator, req, keepAliveJson, keepAliveJsonAction, canary)

	if this.Redirector.UserAgent != "" {
		req.Header.Set("User-Agent", this.Redirector.UserAgent)
	}

	// post something
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Exchange server not available: %s", err)
		// don't invalidate the canary in a network error
		return false
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		log.Printf("Exchange server returned %d status, invalidating canary", resp.StatusCode)
		this.Translator.OwaCanary = ""
		return false
	}

	bodyBytes, err := proxyutils.ReadGzipBody(&resp.Header, resp.Body)
	if err != nil {
		log.Printf("Could not read json response, invalidating canary: %s", err)
		this.Translator.OwaCanary = ""
		return false
	}

	jsonBody := string(bodyBytes)
	if !strings.Contains(jsonBody, "\"ResponseCode\":\"NoError\"") ||
		!strings.Contains(jsonBody, "\"ResponseClass\":\"Success\"") {
		this.Translator.OwaCanary = ""
		return false
	}

	// it was successful, begin the keep alive channel if it doesn't already
	// exist
	if this.KeepAlivePeriod > 0 && this.keepAliveTicker == nil {
		this.keepAliveTicker = time.NewTicker(this.KeepAlivePeriod)
		go this.OwaKeepalive()
	}

	// successful checks
	this.Translator.OwaCanary = canary
	return true
}

func (this *LoginMiddleware) OwaKeepalive() {
	for _ = range this.keepAliveTicker.C {
		if this.Translator.OwaCanary == "" {
			continue
		}

		log.Println("OWA keepalive")

		if !this.CheckLogin(this.Translator.OwaCanary) {
			// only set the status if the canary is unset
			if this.Translator.OwaCanary == "" {
				this.Translator.onTimeout()
			}
		}
	}
}
