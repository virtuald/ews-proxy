package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/TV4/graceful"
	"github.com/pkg/browser"
	"github.com/virtuald/ews-proxy"
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

	transport := ews.NewEwsProxyTransport(source, target)
	transport.Debug = *debug
	if *noverify {
		transport.Transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport

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
