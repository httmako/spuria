package main

import (
	"bytes"
	//"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	//"strings"
	"sync"
	"testing"
	//config
	"io"
	"regexp"
	//"os"
)

var csvTestFile = []byte(`/,echo hi
/testBasic,echo 'hi'
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
/testPost,echo -n '$body'
`)

var testLogger *slog.Logger
var testFuncMap sync.Map
var testMux http.Handler
var testConfig Config

func TestMain(m *testing.M) {
	testConfig = Config{}
	testConfig.IPwhitelist = true
	testConfig.ReplaceParam = true
	testConfig.RateLimit = 10
	testConfig.CsvPath = "routes.example.csv"
	testConfig.WhitelistedIPs = map[string]bool{}
	testConfig.WhitelistedIPs["127.0.0.1"] = true
	testConfig.WhitelistedIPs["2.2.2.2"] = true
	testConfig.ReplaceRegex, _ = regexp.Compile("^[ a-zA-Z0-9/-]*$")
	testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	m.Run()
}

func TestCsvParsing(t *testing.T) {
	LoadRoutesIntoMap(&testFuncMap, csvTestFile, testLogger)
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
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("/ had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "OK" {
		t.Fatal("/ had body != 'OK' : ", body)
	}
}

func TestDontAllowPut(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/testBasic", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 405 {
		t.Fatal("/testBasic had http code != 405", w.Code)
	}
	body := w.Body.String()
	if body != "Method Not Allowed" {
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

func TestParamReplaceNone(t *testing.T) {
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
}

func TestParamReplaceOne(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/testReplace?$parm1=test", nil)
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("req had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "newline\ntest\n$parm2\nhere\n" {
		t.Fatal("req had unexpected body : ", body)
	}
}

func TestParamReplacePostBody(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/testPost", bytes.NewReader([]byte("MYPOSTBODY")))
	req.RemoteAddr = "127.0.0.1:123"
	testMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("req had http code != 200", w.Code)
	}
	body := w.Body.String()
	if body != "MYPOSTBODY" {
		t.Fatal("req had unexpected body: ", body)
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
