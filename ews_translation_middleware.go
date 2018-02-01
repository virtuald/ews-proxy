package ews

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/virtuald/ews-proxy/proxyutils"
)

const ewsContextName = "ews_ctx"

// TranslationMiddleware implements a reverse proxy that allows EWS clients to
// talk to an OWA endpoint
//
type TranslationMiddleware struct {
	// Set to true if you want to see additional logging
	Debug bool

	// default is "/ews/exchange.asmx"
	EwsPath string

	// default is "/owa/service.svc"
	OwaServicePath string

	// OWA Canary value, required for the OWA service to work
	OwaCanary string

	// function pointers controlling various aspects of the transport
	OnEwsSuccess          func()
	OnEwsTimeout          func()
	OnEwsTranslationError func(transactionLog *bytes.Buffer)
}

// Creates an TranslationMiddleware object with lots of defaults filled in
func NewTranslationMiddleware() *TranslationMiddleware {
	transport := &TranslationMiddleware{
		Debug:          false,
		EwsPath:        "/ews/exchange.asmx",
		OwaServicePath: "/owa/service.svc",

		OnEwsSuccess:          func() {},
		OnEwsTimeout:          func() {},
		OnEwsTranslationError: func(*bytes.Buffer) {},
	}

	return transport
}

type ewsProxyContext struct {
	EwsProxyOp     *OpDescriptor
	TransactionLog *bytes.Buffer
}

func (this *TranslationMiddleware) RequestModifier(request *http.Request, cctx proxyutils.ChainContext) error {

	// mangle requests to the EWS path only
	if request.URL.Path != this.EwsPath {
		return nil
	}

	// return empty GET response
	if request.Method == "GET" {
		response := proxyutils.CreateNewResponse(request, "")
		response.StatusCode = http.StatusOK
		return proxyutils.NewRequestError(response)
	}

	// ignore non-POST requests
	if request.Method != "POST" {
		return nil
	}

	// begin the hard work of translation
	ctx := &ewsProxyContext{
		TransactionLog: new(bytes.Buffer),
	}

	// are we authenticated?
	canary := this.OwaCanary
	if canary == "" {

		if this.Debug {
			log.Println("EWS request, but no canary present")
		}

		response := proxyutils.CreateNewResponse(request, "")
		response.StatusCode = 440 // MS LoginTimeout

		// throttle client, as it won't expect this and may keep asking
		time.Sleep(5 * time.Second)

		return proxyutils.NewRequestError(response)
	} else {
		// translate the XML body of the request to JSON
		var ewsRequestData []byte
		var jsonRequestData []byte
		var err error

		ewsRequestData, err = proxyutils.ReadGzipBody(&request.Header, request.Body)
		if err != nil {
			return err
		}

		this.appendTransaction(ctx, "EWS question")
		this.appendTransaction(ctx, string(ewsRequestData))

		jsonRequestData, ctx.EwsProxyOp, err = SOAP2JSON(bytes.NewReader(ewsRequestData))
		if err != nil {
			this.appendTransaction(ctx, "Ews Translator: Request Error: "+err.Error())
			this.OnEwsTranslationError(ctx.TransactionLog)

			// TODO
			// throttle client -- need to slow davmail/macmail down as they won't
			// expect this type of error
			time.Sleep(time.Second)
			return err
		}

		this.appendTransaction(ctx, "OWA JSON question")
		this.appendTransaction(ctx, string(jsonRequestData))

		SetupOwaRequest(this, request, jsonRequestData, ctx.EwsProxyOp.Action, canary)

		// store context for the translation response
		cctx[ewsContextName] = ctx
	}

	return nil
}

func (this *TranslationMiddleware) ResponseModifier(response *http.Response, cctx proxyutils.ChainContext) error {

	var err error

	// if our context isn't present, exit
	if _, ok := cctx[ewsContextName]; !ok {
		return nil
	}

	ctx := cctx["ews_ctx"].(*ewsProxyContext)

	if response.StatusCode == 440 { // MS LoginTimeout
		this.OnEwsTimeout()

	} else if response.StatusCode != http.StatusFound &&
		response.StatusCode != http.StatusGatewayTimeout {
		// translate the response into XML SOAP

		// read it into memory so we can output the json for debug purposes
		var jsonResponseData []byte
		jsonResponseData, err = proxyutils.ReadGzipBody(&response.Header, response.Body)
		if err != nil {
			return err
		}

		this.appendTransaction(ctx, "OWA JSON response:")
		this.appendTransaction(ctx, string(jsonResponseData))

		outbuf := new(bytes.Buffer)
		err = JSON2SOAP(bytes.NewReader(jsonResponseData), ctx.EwsProxyOp, outbuf, false)
		if err != nil {
			this.appendTransaction(ctx, "Ews Translator: Response Error: "+err.Error())
			this.OnEwsTranslationError(ctx.TransactionLog)

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

	return err
}

func SetupOwaRequest(translator *TranslationMiddleware, request *http.Request, json []byte, action string, canary string) {
	// replace the body content with the JSON, set appropriate lengths
	request.Body = ioutil.NopCloser(bytes.NewReader(json))
	request.ContentLength = int64(len(json))
	request.Header.Set("Content-Length", strconv.Itoa(len(json)))
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	request.URL.Path = translator.OwaServicePath

	// set the needed OWA headers
	request.Header.Set("Action", action)
	request.Header.Set("X-OWA-Canary", canary)
	// OWA accepts either this header or POST data in the body
	// -> prefer the POST body
	//request.Header.Set("X-OWA-UrlPostData", url.PathEscape(string(jsonRequestData)))
}

func (this *TranslationMiddleware) appendTransaction(cxt *ewsProxyContext, content string) {
	if this.Debug {
		log.Println(content)
	}

	cxt.TransactionLog.WriteString(content)
	cxt.TransactionLog.WriteRune('\n')
}
