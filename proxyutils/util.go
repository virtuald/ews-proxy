package proxyutils

import (
	"compress/gzip"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/pkg/errors"
)

func CreateNewResponse(request *http.Request, content string) *http.Response {
	response := &http.Response{
		Request: request,
		Header:  http.Header{},
	}

	response.Body = ioutil.NopCloser(strings.NewReader(content))
	response.ContentLength = int64(len(content))
	response.Proto = request.Proto
	response.ProtoMajor = request.ProtoMajor
	response.ProtoMinor = request.ProtoMinor
	response.StatusCode = http.StatusOK
	return response
}

// utility function that reads the bytes from either a request or a response
// and returns them. Handles gzip compression if present
func ReadGzipBody(header *http.Header, body io.ReadCloser) ([]byte, error) {

	var theReader io.ReadCloser
	var err error

	if header.Get("Content-Encoding") == "gzip" {
		// we never gzip anything
		header.Del("Content-Encoding")

		theReader, err = gzip.NewReader(body)
		if err != nil {
			return nil, errors.Wrapf(err, "open gzip reader")
		}

		defer theReader.Close()

	} else {
		theReader = body
	}

	// Get the data (through the set reader)
	b, err := ioutil.ReadAll(theReader)
	if err != nil {
		return nil, errors.Wrapf(err, "reading body")
	}

	// Close the reader
	err = body.Close()
	if err != nil {
		return nil, errors.Wrapf(err, "closing response reader")
	}

	return b, nil
}
