package main

import (
	// "bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	//config
	"io"
	"os"
)

var csvTestFile = []byte(`/testBasic,echo 'hi'
/testMultiline,"echo 'newline
text
file
here'"
/testReplace,"echo 'newline
$parm1
$parm2
here'"
/testError,somethingthatdoesntexist
/testFuzz,echo -n '$parm1'
`)

var flagArgs = []string{"-routes", "routes.example.csv", "-replaceparam", "-allowedips", "127.0.0.1,2.2.2.2"}

var testLogger *slog.Logger
var testFuncMap sync.Map
var testMux http.Handler
var testConfig Config

func TestConfigParsing(t *testing.T) {
	config, _, err := parseFlags(os.Args[0], flagArgs)
	if err != nil {
		t.Fatal("Error during parseFlags", err)
	}
	testConfig = *config
	if testConfig.CsvPath == "" {
		t.Fatal("Missing CsvPath")
	}
	if !testConfig.WhitelistedIPs["127.0.0.1"] {
		t.Fatal("Missing 127.0.0.1 in IP whitelist")
	}
	if !testConfig.WhitelistedIPs["2.2.2.2"] {
		t.Fatal("Missing 2.2.2.2 in IP whitelist")
	}
}

func TestCsvParsing(t *testing.T) {
	testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	err := LoadRoutesIntoMap(&testFuncMap, csvTestFile, testLogger)
	if err != nil {
		t.Fatal("Error LoadRoutesIntoMap", err)
	}
	if _, exists := testFuncMap.Load("/testBasic"); !exists {
		t.Fatal("funcMap missed /testBasic")
	}
	if _, exists := testFuncMap.Load("/testMultiline"); !exists {
		t.Fatal("funcMap missed /testMultiline")
	}
	if _, exists := testFuncMap.Load("/testReplace"); !exists {
		t.Fatal("funcMap missed /testReplace")
	}
}

func TestServerCreation(t *testing.T) {
	testMux = NewServer(&testConfig, &testFuncMap, testLogger)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("/ had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "" {
		t.Fatal("/ had body != '' : ", body)
	}
}

func TestDontAllowPost(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/testBasic", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 405 {
		t.Fatal("/testBasic had http code != 405", w.Code)
	}
	body := w.Body.String()
	if body != "Method Not Allowed\n" {
		t.Fatal("/testBasic had body != 'Method Not Allowed' : ", body)
	}
}

func TestBasicNonVerbose(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testBasic", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("/testBasic had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "OK" {
		t.Fatal("/testBasic had body != 'OK' : ", body)
	}
}

func TestForError(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testError", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatal("/testError had http code != 500", w.Code)
	}
	body := w.Body.String()
	if body != "ERR" {
		t.Fatal("/testError had body != 'ERR' : ", body)
	}
}

func TestIPWhitelist(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testBasic", nil)
	req.RemoteAddr = "3.3.3.3:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("req had http code != 403", w.Code)
	}
	body := w.Body.String()
	if body != "NOACCESS" {
		t.Fatal("req had body != 'NOACCESS' : ", body)
	}
}

func TestNoRoute(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/nonExisting", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("req had http code != 404", w.Code)
	}
	body := w.Body.String()
	if body != "URL not found or configured! (\"/nonExisting\")" {
		t.Fatal("req had unexpected body : ", body)
	}
}

func TestVerbose(t *testing.T) {
	testConfig.ReturnResult = true
	testMux = NewServer(&testConfig, &testFuncMap, testLogger)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testBasic", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("req had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "hi\n" {
		t.Fatal("req had body != 'hi/n' : ", body)
	}
}

func TestVerboseError(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testError", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatal("/testError had http code != 500", w.Code)
	}
	body := w.Body.String()
	if body != "bash: line 1: somethingthatdoesntexist: command not found\n" {
		t.Fatal("/testError had unexpected body : ", body)
	}
}

func TestMultiline(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testMultiline", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("req had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "newline\ntext\nfile\nhere\n" {
		t.Fatal("req had unexpected body : ", body)
	}
}

func TestParamReplace(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testReplace", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("req had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "newline\n$parm1\n$parm2\nhere\n" {
		t.Fatal("req had unexpected body : ", body)
	}

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/testReplace?$parm1=test", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("req had http code != 200", w.Code)
	}
	body = w.Body.String()
	if body != "newline\ntest\n$parm2\nhere\n" {
		t.Fatal("req had unexpected body : ", body)
	}
}

func TestParamOnlyReplaceDollar(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testReplace?newline=test", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatal("req had http code != 500", w.Code)
	}
	body := w.Body.String()
	if body != "" {
		t.Fatal("req had unexpected body : ", body)
	}
}

func TestSpamProtection(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testMultiline", nil)
	req.RemoteAddr = "127.0.0.1:123"
	for i := 0; i < 10; i++ {
		testMux.ServeHTTP(w, req)
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/testMultiline", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 429 {
		t.Fatal("req had http code != 429", w.Code)
	}
	body := w.Body.String()
	if body != "" {
		t.Fatal("req had unexpected body : ", body)
	}
}

func TestSpamProtectionNotForOthers(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testBasic", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("req had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "hi\n" {
		t.Fatal("req had unexpected body : ", body)
	}
}

func FuzzParamReplace(f *testing.F) {
	for _, seed := range [][]byte{{}, {0}, {9}, {0xa}, {0xf}, {1, 2, 3, 4}} {
		f.Add(seed)
	}

	testLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	config, _, err := parseFlags(os.Args[0], flagArgs)
	if err != nil {
		fmt.Println(err)
		return
	}
	config.RateLimit = 99999999
	config.ReturnResult = true
	funcMap := sync.Map{}
	err = LoadRoutesIntoMap(&funcMap, csvTestFile, testLogger)
	if err != nil {
		fmt.Println(err)
		return
	}
	testMux := NewServer(config, &funcMap, testLogger)

	f.Fuzz(func(t *testing.T, in []byte) {
		stringIn := string(in)
		w := httptest.NewRecorder()
		req, err := http.NewRequest("GET", "/testFuzz?$parm1="+stringIn, nil)
		if err != nil {
			// t.Fatal(err)
			return
		}
		req.RemoteAddr = "127.0.0.1:123"
		testMux.ServeHTTP(w, req)
		body := w.Body.String()
		if body != stringIn && body != "$parm1" && //Same input or unchanged input, is ok
			(body != "" && w.Code != 500) && //Filtered by regex, is ok
			!strings.Contains(stringIn, "#") && //javascript # having different in-to-out is ok
			!strings.Contains(stringIn, "&") && //filtered out by net/http
			!strings.Contains(stringIn, "+") && //equals space, filtered out
			!strings.Contains(stringIn, "%") { //equals special e.g. space, filtered out
			t.Fatal("Bad input found:", "'"+body+"'", "'"+stringIn+"'")
		}
	})
}
