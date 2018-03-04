package proxyutils

import (
	"log"
	"net/http"
	"time"
)

// used because the golang context stuff is weird...
type ChainContext map[interface{}]interface{}

type RequestModifierFunc func(*http.Request, ChainContext) error
type ResponseModifierFunc func(*http.Response, ChainContext) error

type Middleware interface {

	// Modifies the request
	RequestModifier(*http.Request, ChainContext) error

	// Modifies the response
	ResponseModifier(*http.Response, ChainContext) error
}

// an error that contains a new response to send to the client
type RequestError struct {
	Response *http.Response
}

func (this *RequestError) Error() string {
	return "Not an error"
}

func NewRequestError(response *http.Response) error {
	return &RequestError{Response: response}
}

type chainedProxy struct {
	Name string
	
	LogTrace *log.Logger
	LogDebug *log.Logger
	LogInfo *log.Logger
	LogWarn *log.Logger
	LogError *log.Logger

	RequestModifiers  []RequestModifierFunc
	ResponseModifiers []ResponseModifierFunc

	Transport http.RoundTripper
}

// returns a http.RoundTripper that calls each middleware in order
func CreateChainedProxy(name string, 
	logTrace *log.Logger,
	logDebug *log.Logger,
	logInfo *log.Logger,
	logWarn *log.Logger,
	logError *log.Logger,
	Transport http.RoundTripper, middlewares ...Middleware) http.RoundTripper {
		 
	if Transport == nil {
		Transport = http.DefaultTransport
	}

	proxy := &chainedProxy{
		Name:    name,
		LogTrace: logTrace,
		LogDebug: logDebug,
		LogInfo: logInfo,
		LogWarn: logWarn,
		LogError: logError,
		Transport: Transport,
	}

	// separate the modifiers to make RoundTrip easier
	for _, middleware := range middlewares {
		proxy.RequestModifiers = append(proxy.RequestModifiers, middleware.RequestModifier)

		// prepend for reverse order
		proxy.ResponseModifiers = append([]ResponseModifierFunc{middleware.ResponseModifier}, proxy.ResponseModifiers...)
	}

	return proxy
}

// Passes the http.Request through all of the request handlers, sends to the
// remote server, then passes through all of the response handlers
func (this *chainedProxy) RoundTrip(request *http.Request) (*http.Response, error) {

	this.LogInfo.Println(this.Name, request.Method, request.URL.Path)

	var response *http.Response
	var err error

	defer func() {
		if response != nil {
			this.LogInfo.Println(this.Name, "response", response.StatusCode)
		}
	}()

	ctx := make(ChainContext)

	// first pass through anyone who wants to modify this
	for _, modifier := range this.RequestModifiers {
		if err = modifier(request, ctx); err != nil {
			if re, ok := err.(*RequestError); ok {
				return re.Response, nil
			} else {
				return nil, err
			}
		}
	}
	
	// try each connection up to 3 times
  retryCount := 3
  for retryCount > 0 {
    response, err = this.Transport.RoundTrip(request)
    if err == nil {
      // success, stop trying
      break
    } else {
      this.LogWarn.Println(this.Name, "Network error, retrying: ", err)
      // throttle
      time.Sleep(1 * time.Second)
    }
    retryCount -= 1;
  }
	
	if err != nil {
		// this is always some sort of network error, but let's choose to return a
		// valid response to the client telling them what happened...
		response = CreateNewResponse(request, "")
		response.StatusCode = http.StatusGatewayTimeout
		return response, nil
	}
	
	// anybody want to modify the response?
	for _, modifier := range this.ResponseModifiers {
		err = modifier(response, ctx)
		if err != nil {
			return nil, err
		}
	}

	return response, err
}
