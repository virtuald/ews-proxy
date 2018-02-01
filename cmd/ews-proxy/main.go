package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/TV4/graceful"
	"github.com/pkg/browser"
	"github.com/virtuald/ews-proxy"
	"github.com/virtuald/ews-proxy/proxyutils"
)

func main() {

	debug := flag.Bool("debug", false, "Enable extra debug logging")
	noverify := flag.Bool("noverify", false, "Disable HTTPS certificate verfication")
	listenPort := flag.Int("listenPort", 60001, "Port to listen on")

	flag.Parse()

	exchangeServer := flag.Arg(0)
	if exchangeServer == "" {
		log.Println("Error: must specify exchange server")
		return
	}

	target, err := url.Parse(exchangeServer)
	if err != nil {
		log.Printf("Error parsing exchange server: %s", err)
		return
	}

	// fixup target
	if target.Scheme == "" || target.Host == "" {
		log.Printf("Invalid exchange server URL '%s'", exchangeServer)
		return
	}
	
	source, _ := url.Parse(fmt.Sprintf("http://localhost:%d", *listenPort))

	// construct the HTTP transport
	dialer := net.Dialer{Timeout: 2 * time.Second}

	transport := &http.Transport{Dial: dialer.Dial}
	if *noverify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	// construct the needed middlewares
	redirector := proxyutils.NewRedirectorMiddleware(source, target)
	
	translator := ews.NewTranslationMiddleware()
	translator.Debug = *debug
	
	
	login := &ews.LoginMiddleware{
		Redirector: redirector,
		Translator: translator,
		Transport: transport,
		CheckPath: "/owa/",
	}
	
	// create a chained reverse proxy
	chain := proxyutils.CreateChainedProxy("EWS Proxy", transport, login, translator, redirector)
	
	proxy := &httputil.ReverseProxy{
		Director: func(*http.Request){},
		Transport: chain,
	}
	
	// navigate to listening port after the server starts
	go func() {
		time.Sleep(1 * time.Second)
		openUrl := fmt.Sprintf("http://localhost:%d/owa/", *listenPort)
		browser.OpenURL(openUrl)
	}()

	graceful.LogListenAndServe(&http.Server{
		Addr:    fmt.Sprintf("localhost:%d", *listenPort),
		Handler: proxy,
	})
}
