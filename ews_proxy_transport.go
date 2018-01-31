package ews

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// EwsProxyTransport implements a reverse proxy that allows EWS clients to
// talk to an OWA endpoint
//
type EwsProxyTransport struct {
	// Set to true if you want to see additional logging
	Debug bool

	// default is "/ews/exchange.asmx"
	EwsPath string

	// default is "/owa/service.svc"
	OwaServicePath string

	// Remote Exchange server URL
	TargetServer *url.URL
	
	// the host:port that the reverse proxy is listening on
	SourceServer *url.URL

	// shared transport object for connections
	Transport *http.Transport

	// Set this to something to override the UserAgent sent to the remote site
	UserAgent string

	// in-memory holder of cookies to be applied to the session
	Cookies http.CookieJar

	// OWA Canary value, required for the OWA service to work
	OwaCanary string

	// function pointers controlling various aspects of the transport
	OnEwsSuccess          func()
	OnEwsTimeout          func()
	OnEwsTranslationError func(transactionLog *bytes.Buffer)

	OnNetworkError func(response *http.Response, err error)
	OnRedirect     func(response *http.Response)

	// use these two to obtain the canary
	OnUnhandledPath         func(request *http.Request) (*http.Response, error)
	OnUnhandledPathResponse func(response *http.Response, cookies []*http.Cookie)

	// disabled if 0
	KeepAlivePeriod time.Duration
	keepAliveTicker *time.Ticker
}

// Creates an EwsProxyTransport object with lots of defaults filled in
func NewEwsProxyTransport(source *url.URL, target *url.URL) *EwsProxyTransport {
	cookies, _ := cookiejar.New(nil)
	dialer := net.Dialer{Timeout: 2 * time.Second}
	transport := &EwsProxyTransport{
		Debug:          false,
		EwsPath:        "/ews/exchange.asmx",
		OwaServicePath: "/owa/service.svc",
		SourceServer:   source,
		TargetServer:   target,
		Transport: &http.Transport{
			Dial: dialer.Dial,
		},
		Cookies:               cookies,
		OnEwsSuccess:          func() {},
		OnEwsTimeout:          func() {},
		OnEwsTranslationError: func(*bytes.Buffer) {},
		OnNetworkError:        func(*http.Response, error) {},
		OnRedirect:            func(*http.Response) {},
		KeepAlivePeriod:       3 * time.Minute,
	}

	transport.OnUnhandledPathResponse = transport.DefaultUnhandledPathResponse
	return transport
}


// reverse proxy function
func (this *EwsProxyTransport) RoundTrip(request *http.Request) (*http.Response, error) {

	log.Println("EwsProxy:", request.Method, request.URL.Path)
	
	// special redirect -- tell the user to close the page
	if request.URL.Path == "/close.html" {
		response := this.createEmptyResponse(request, closePageHtml)
		return response, nil
	}
	
	// mangle the request in various ways
	request.Header.Del("X-Forwarded-For")
	request.Header.Del("Upgrade-Insecure-Requests")
	// don't forward any cookies from the client
	request.Header.Del("Cookie")
	
	// Fix various headers that may contain a URL
	retargetHeader(&request.Header, "Origin", this.TargetServer)
	retargetHeader(&request.Header, "Referer", this.TargetServer)
	
	// optionally mangle the User-Agent header
	userAgent := this.UserAgent
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}

	var response *http.Response
	var err error = nil
	
	// set any stored cookies
	for _, cookie := range this.Cookies.Cookies(this.TargetServer) {
		request.AddCookie(cookie)
	}

	// mangle requests to the EWS path
	if request.URL.Path == this.EwsPath {

		switch request.Method {
		// if it's a POST, translate it
		case "POST":
			response, err = this.translateEws(request)
			break

		case "GET":
			response = this.createEmptyResponse(request, "")
			response.StatusCode = http.StatusOK
			break
		}
	}

	// if it's not, then just pass it straight through
	var cookies []*http.Cookie

	if response == nil && err == nil {
		if this.OnUnhandledPath != nil {
			response, err = this.OnUnhandledPath(request)
		}

		if response == nil && err == nil {
			response, err = this.forwardRequest(request)
		}

		if response != nil && this.OnUnhandledPathResponse != nil {
			cookies = response.Cookies()
			this.OnUnhandledPathResponse(response, cookies)
		}
	}

	if response != nil {
		log.Println("EwsProxy: response", response.StatusCode)

		// steal all the cookies, don't expose them to the client
		if cookies == nil {
			cookies = response.Cookies()
		}

		this.Cookies.SetCookies(this.TargetServer, cookies)
		response.Header.Del("Set-Cookie")
	}

	return response, err
}

func (this *EwsProxyTransport) createEmptyResponse(request *http.Request, content string) *http.Response {
	response := &http.Response{
		Request: request,
		Header:  http.Header{},
	}

	response.Body = ioutil.NopCloser(strings.NewReader(content))
	response.ContentLength = int64(len(content))
	response.Proto = request.Proto
	response.ProtoMajor = request.ProtoMajor
	response.ProtoMinor = request.ProtoMinor
	return response
}

//
// forwards the proxied request to the destination server, dealing
// with network errors
//
func (this *EwsProxyTransport) forwardRequest(request *http.Request) (*http.Response, error) {

	// fix the outgoing request
	origHost := request.Host
	request.Host = this.TargetServer.Host
	request.URL.Host = this.TargetServer.Host
	request.URL.Scheme = this.TargetServer.Scheme

	// try each connection up to 3 times because of potential network issues
	var err error
	var response *http.Response
	retryCount := 3
	for retryCount > 0 {
		response, err = this.Transport.RoundTrip(request)
		if err == nil {
			// success, stop trying
			break
		} else {
			log.Println("Network error, retrying: ", err)
			// throttle
			time.Sleep(1 * time.Second)
		}
		retryCount -= 1
	}

	if err != nil {
		// this is always some sort of network error, but we have to return a
		// valid response to the client...
		response = this.createEmptyResponse(request, "")
		response.StatusCode = http.StatusGatewayTimeout

		this.OnNetworkError(response, err)
		err = nil

		// always throttle network errors
		time.Sleep(1 * time.Second)

	} else if response.StatusCode == http.StatusFound {
		// on a 302, redirect back to this server, not to the proxied server
		retargetHeader(&response.Header, "Location", this.SourceServer)
		this.OnRedirect(response)
	}

	// restore the Host header
	response.Header.Set("Host", origHost)
	return response, err
}

func (this *EwsProxyTransport) translateEws(request *http.Request) (*http.Response, error) {

	// used for EWS translation
	var ewsProxyOp *OpDescriptor

	// used to output debug information in case of an error
	transactionLog := new(bytes.Buffer)

	// are we authenticated?
	canary := this.OwaCanary
	if canary == "" {

		if this.Debug {
			log.Println("EWS request, but no canary present")
		}

		response := this.createEmptyResponse(request, "")
		response.StatusCode = 440 // MS LoginTimeout

		// throttle client, as it won't expect this and may keep asking
		time.Sleep(5 * time.Second)

		return response, nil
	} else {
		// translate the XML body of the request to JSON
		var ewsRequestData []byte
		var jsonRequestData []byte
		var err error

		ewsRequestData, err = ioutil.ReadAll(request.Body)
		request.Body.Close()

		if err != nil {
			return nil, err
		}

		this.appendTransaction(transactionLog, "EWS question")
		this.appendTransaction(transactionLog, string(ewsRequestData))

		jsonRequestData, ewsProxyOp, err = SOAP2JSON(bytes.NewReader(ewsRequestData))
		if err != nil {
			log.Println("Ews Translator: Request Error", err)

			this.appendTransaction(transactionLog, "Ews Translator: Request Error: "+err.Error())
			this.OnEwsTranslationError(transactionLog)

			// TODO
			// throttle client -- need to slow davmail/macmail down as they won't
			// expect this type of error
			time.Sleep(time.Second)
			return nil, err
		}

		this.appendTransaction(transactionLog, "OWA JSON question")
		this.appendTransaction(transactionLog, string(jsonRequestData))

		this.SetupOwaRequest(request, jsonRequestData, ewsProxyOp.Action, canary)
	}

	response, err := this.forwardRequest(request)
	if err != nil {
		return response, err
	}

	if response.StatusCode == 440 { // MS LoginTimeout
		this.OnEwsTimeout()

	} else if response.StatusCode != http.StatusFound &&
		response.StatusCode != http.StatusGatewayTimeout {
		// translate the response into XML SOAP

		// read it into memory so we can output the json for debug purposes
		var jsonResponseData []byte
		jsonResponseData, err = ioutil.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}

		this.appendTransaction(transactionLog, "OWA JSON response:")
		this.appendTransaction(transactionLog, string(jsonResponseData))

		outbuf := new(bytes.Buffer)
		err = JSON2SOAP(bytes.NewReader(jsonResponseData), ewsProxyOp, outbuf, false)
		if err != nil {
			log.Println("Ews Translator: Response Error", err)

			this.appendTransaction(transactionLog, "Ews Translator: Response Error: "+err.Error())
			this.OnEwsTranslationError(transactionLog)

			response.StatusCode = http.StatusInternalServerError
			response.Header.Set("X-EwsProxyError", fmt.Sprintf("%s", err))
			response.Body = ioutil.NopCloser(bytes.NewReader(jsonResponseData))
			response.ContentLength = int64(len(jsonResponseData))

			// throttle client -- need to slow davmail/macmail down as they won't
			// expect this type of error
			time.Sleep(time.Second)
			err = nil

		} else {
			response.Header.Set("Content-Type", "text/xml; charset=utf-8")
			response.Body = ioutil.NopCloser(outbuf)
			response.ContentLength = int64(outbuf.Len())

			if response.StatusCode == http.StatusOK {
				this.OnEwsSuccess()
			}
		}
	}

	return response, err
}

func (this *EwsProxyTransport) SetupOwaRequest(request *http.Request, json []byte, action string, canary string) {
	// replace the body content with the JSON, set appropriate lengths
	request.Body = ioutil.NopCloser(bytes.NewReader(json))
	request.ContentLength = int64(len(json))
	request.Header.Set("Content-Length", strconv.Itoa(len(json)))
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	request.URL.Path = this.OwaServicePath

	// set the needed OWA headers
	request.Header.Set("Action", action)
	request.Header.Set("X-OWA-Canary", canary)
	// OWA accepts either this header or POST data in the body
	// -> prefer the POST body
	//request.Header.Set("X-OWA-UrlPostData", url.PathEscape(string(jsonRequestData)))
}

func (this *EwsProxyTransport) appendTransaction(transactionLog *bytes.Buffer, content string) {
	if this.Debug {
		log.Println(content)
	}

	transactionLog.WriteString(content)
	transactionLog.WriteRune('\n')
}

// This processes /owa/ pages and searches for a valid OWA canary. Once the
// canary has been found, then it stops all other OWA accesses
func (this *EwsProxyTransport) DefaultUnhandledPathResponse(response *http.Response, cookies []*http.Cookie) {
	// Watch for OWA Canary info, and snag it
	requrl := response.Request.URL.String()
	if strings.Contains(requrl, "/owa/") && response.StatusCode != 302 {
		for _, cookie := range cookies {
			if cookie.Name == "X-OWA-CANARY" {
				// if the user agent isn't set, set it since this access is being
				// done by a user's browser
				if this.UserAgent == "" {
					this.UserAgent = response.Header.Get("User-Agent")
				}

				// validate and set the canary if it's valid
				this.CheckLogin(cookie.Value)
				break
			}
		}

		// If we have a canary stored, _always_ tell the user's page to close, otherwise
		// eventually they'll make it to the OWA page
		if this.OwaCanary != "" {
			this.OnEwsSuccess()

			response.Body = ioutil.NopCloser(strings.NewReader(""))
			response.ContentLength = 0

			response.Header = http.Header{}
			response.Header.Set("Location", "/close.html")
			response.StatusCode = http.StatusFound
		}
	}
}

// CheckLogin returns false if login is required, and will
// invalidate the canary if the server responds that it is invalid
func (this *EwsProxyTransport) CheckLogin(canary string) bool {

	if canary == "" {
		return false
	}

	client := http.Client{Transport: this.Transport}
	client.Jar = this.Cookies

	req, err := http.NewRequest("POST", this.TargetServer.ResolveReference(&url.URL{Path: this.OwaServicePath}).String(), nil)
	if err != nil {
		log.Printf("Error checking OWA: %s", err)
		this.OwaCanary = ""
		return false
	}

	this.SetupOwaRequest(req, keepAliveJson, keepAliveJsonAction, canary)

	if this.UserAgent != "" {
		req.Header.Set("User-Agent", this.UserAgent)
	}

	// post something
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Exchange server not available: %s", err)
		// don't invalidate the canary in a network error
		return false
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("Exchange server returned %d status, invalidating canary", resp.StatusCode)
		this.OwaCanary = ""
		return false
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Could not read json response, invalidating canary: %s", err)
		this.OwaCanary = ""
		return false
	}

	jsonBody := string(bodyBytes)
	if !strings.Contains(jsonBody, "\"ResponseCode\":\"NoError\"") ||
		!strings.Contains(jsonBody, "\"ResponseClass\":\"Success\"") {
		this.OwaCanary = ""
		return false
	}

	// it was successful, begin the keep alive channel if it doesn't already
	// exist
	if this.KeepAlivePeriod > 0 && this.keepAliveTicker == nil {
		// TODO: make keepalive adjustable
		this.keepAliveTicker = time.NewTicker(this.KeepAlivePeriod)
		go this.OwaKeepalive()
	}

	// successful checks
	this.OwaCanary = canary
	return true
}

func (this *EwsProxyTransport) OwaKeepalive() {
	for _ = range this.keepAliveTicker.C {
		if this.OwaCanary == "" {
			continue
		}

		log.Println("OWA keepalive")

		if !this.CheckLogin(this.OwaCanary) {
			// only set the status if the canary is unset
			if this.OwaCanary == "" {
				this.OnEwsTimeout()
			}
		}
	}
}

// utility function that retargets headers that have a URL in them
func retargetHeader(header *http.Header, name string, newUrl *url.URL) {
	origStr := header.Get(name)
	if origStr != "" {
		hUrl, _ := url.Parse(origStr)
		if hUrl != nil {
			hUrl.Scheme = newUrl.Scheme
			hUrl.Host = newUrl.Host
			header.Set(name, hUrl.String())
		}
	}
}

