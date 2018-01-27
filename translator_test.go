package ews

import (
	diff "github.com/yudai/gojsondiff"
	"github.com/yudai/gojsondiff/formatter"

	"github.com/pkg/errors"
	"github.com/sergi/go-diff/diffmatchpatch"

	"bufio"
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// https://stackoverflow.com/questions/5884154/read-text-file-into-string-array-and-write
func readXfail(fname string) (ret map[string]bool) {

	ret = make(map[string]bool)

	file, err := os.Open(fname)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if text := strings.TrimSpace(scanner.Text()); len(text) != 0 {
			ret[text] = true
		}
	}
	return
}

func shouldFail(xfail map[string]bool, fname string) (ret bool) {
	if _, ok := xfail[filepath.Base(fname)]; ok {
		return true
	}
	return false
}

// borrowed from https://github.com/yudai/gojsondiff/blob/master/jd/main.go
// .. why isn't there a utility function for this?
// .. returns true on error/modified, false otherwise
func diffJson(a []byte, b []byte) (diffString string, err error) {

	// the diff library seems to be buggy (but still useful for showing
	// the output), so use Reflect instead

	var aj map[string]interface{}
	var bj map[string]interface{}

	if err = json.Unmarshal(a, &aj); err != nil {
		return "", errors.Wrap(err, "A json error")
	}

	if err = json.Unmarshal(b, &bj); err != nil {
		return "", errors.Wrap(err, "B json error")
	}

	if reflect.DeepEqual(aj, bj) {
		return "", nil
	}

	differ := diff.New()

	d, err := differ.Compare(a, b)
	if err != nil {
		return "", errors.Wrap(err, "json diff failed")
	}

	if d.Modified() {
		config := formatter.AsciiFormatterConfig{
			ShowArrayIndex: true,
			Coloring:       true,
		}

		dformat := formatter.NewAsciiFormatter(aj, config)

		diffString, err = dformat.Format(d)
		if err != nil {
			return "", errors.Wrap(err, "json diff format failed")
		}
	}

	err = errors.New("A and B are different")
	return
}

type TestFunc func(string) (string, error)

func testRunner(t *testing.T, globpath string, fn TestFunc) {
	var testfiles []string

	// for debugging only
	envTestfile := os.Getenv("EWS_TESTFILE")
	if envTestfile != "" {
		testfiles = []string{envTestfile}
	} else {
		// normally get them from the testdata directory
		var err error
		testfiles, err = filepath.Glob(globpath)
		if err != nil {
			t.Fatal(err)
		}
	}

	xfailMap := readXfail(filepath.Join(filepath.Dir(globpath), "xfail"))

	sort.Strings(testfiles)

	passed := 0

	for _, testfile := range testfiles {

		xfail := shouldFail(xfailMap, testfile)
		xfailStr := ""
		if xfail {
			xfailStr = " (should fail)"
		}

		t.Logf("Now testing %s%s", testfile, xfailStr)

		diffString, err := fn(testfile)
		if err != nil {
			if xfail {
				passed++
				t.Log(err)
				if diffString != "" {
					t.Log(diffString)
				}
			} else {
				t.Errorf("Failed: %s", err)
				if diffString != "" {
					t.Error(diffString)
				}
			}
		} else {
			if xfail {
				t.Errorf("Expected failure (did not fail)")
			} else {
				passed++
			}
		}
	}

	t.Logf("%d/%d tests passed", passed, len(testfiles))
	if passed == 0 {
		t.Fail()
	}
}

func testSoapToJsonSingle(testfile string) (diffstring string, err error) {
	xmlReader, err := os.Open(testfile)
	if err != nil {
		return "", errors.Wrapf(err, "opening %s", testfile)
	}

	defer xmlReader.Close()

	data, _, err := SOAP2JSON(xmlReader)
	if err != nil {
		return "", errors.Wrapf(err, "parse failed %s", testfile)
	}

	// if you need the contents of the file
	//ioutil.WriteFile(testfile + ".gen.json", data, 0700)

	// load the correct output from a file
	buf, err := ioutil.ReadFile(testfile + ".json")
	if err != nil {
		return "", errors.Wrapf(err, "%s.json load failed %s", testfile)
	}

	return diffJson(buf, data)
}

func TestSOAP2JSON(t *testing.T) {
	testRunner(t, filepath.Join("testdata", "requests", "*.xml"), testSoapToJsonSingle)
}

func testJson2SoapSingle(testfile string) (diffstring string, err error) {
	dmp := diffmatchpatch.New()

	jsonReader, err := os.Open(testfile)
	if err != nil {
		return "", errors.Wrapf(err, "Opening %s", testfile)
	}

	defer jsonReader.Close()

	// in order to process a response, we have to know what operation it is,
	// so we encode it as the first part of the filename
	opname := strings.Split(strings.Split(filepath.Base(testfile), ".")[0], "_")[0]
	op := EwsOperations[opname]
	if op == nil {
		return "", errors.Errorf("unknown EWS operation `%s` in `%s`", opname, testfile)
	}

	buf := new(bytes.Buffer)
	err = JSON2SOAP(jsonReader, op, buf, true)
	if err != nil {
		return "", errors.Wrapf(err, "parsing `%s` failed", testfile)
	}

	// if you need the contents of the file
	//ioutil.WriteFile(testfile + ".gen.xml", buf.Bytes(), 0700)

	// we're cheating here -- just going to do a text comparison of the XML,
	// since the output should be fairly deterministic. It would be nice to
	// do a logical comparison instead... but we need something for now

	// load the correct output from a file
	correctBuf, err := ioutil.ReadFile(testfile + ".xml")
	if err != nil {
		return "", errors.Wrapf(err, "loading `%s.xml` failed", testfile)
	}

	// if they match, then we're good to go
	if bytes.Compare(buf.Bytes(), correctBuf) == 0 {
		return "", nil
	} else {
		// display a diff
		// TODO: this diff ignores whitespace, which happens to
		//       be really annoying if the outputs only differ by whitespace
		diffs := dmp.DiffMain(string(correctBuf), string(buf.Bytes()), true)
		diffText := dmp.DiffPrettyText(diffs)
		return diffText, errors.New("outputs are different")
	}
}

func TestJSON2SOAP(t *testing.T) {
	testRunner(t, filepath.Join("testdata", "responses", "*.json"), testJson2SoapSingle)
}
