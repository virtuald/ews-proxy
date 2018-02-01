package proxyutils

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
)

// RedirectorMiddleware is a reverse proxy that hides details from both the source client
// and the target server
type RedirectorMiddleware struct {

	// in-memory holder of cookies to be applied to the session
	// -> URL is TargetServer
	Cookies http.CookieJar

	// If a Location: header is encountered, use this to figure out how to handle it
	RetargetMap RetargetMap

	// Remote
	TargetServer *url.URL

	// the host:port that the proxy is listening on
	SourceServer *url.URL

	// Set this to something to override the UserAgent sent to the remote site
	UserAgent string
}

func NewRedirectorMiddleware(source *url.URL, target *url.URL) *RedirectorMiddleware {

	cookies, _ := cookiejar.New(nil)

	proxy := &RedirectorMiddleware{
		Cookies:      cookies,
		RetargetMap:  make(RetargetMap),
		SourceServer: source,
		TargetServer: target,
	}

	// seed the RetargetMap
	proxy.RetargetMap.Add(source, target)
	return proxy
}

// this implements the http.RoundTripper interface, but we break a lot of the
// rules as we modify the request significantly
func (this *RedirectorMiddleware) RequestModifier(request *http.Request, ctx ChainContext) error {

	// mangle the request in various ways
	request.Header.Del("X-Forwarded-For")
	request.Header.Del("Upgrade-Insecure-Requests")

	// don't forward any cookies from the client
	request.Header.Del("Cookie")
	for _, cookie := range this.Cookies.Cookies(this.TargetServer) {
		request.AddCookie(cookie)
	}

	// Fix various headers that may contain a URL
	this.RetargetMap.Retarget(&request.Header, "Origin", this.TargetServer)
	this.RetargetMap.Retarget(&request.Header, "Referer", this.TargetServer)

	// retarget the request itself
	ctx["maskcxt_host"] = request.Host
	request.Host = this.TargetServer.Host
	request.URL.Host = this.TargetServer.Host
	request.URL.Scheme = this.TargetServer.Scheme
	return nil
}

func (this *RedirectorMiddleware) ResponseModifier(response *http.Response, ctx ChainContext) error {
	// If there's a location header, redirect back to this server, not to the target
	this.RetargetMap.Retarget(&response.Header, "Location", this.SourceServer)

	// steal all the cookies, don't expose them to the client
	if cookies := response.Cookies(); cookies != nil {
		this.Cookies.SetCookies(this.TargetServer, cookies)
		response.Header.Del("Set-Cookie")
	}

	// restore the Host header
	response.Header.Set("Host", ctx["maskcxt_host"].(string))
	return nil
}
