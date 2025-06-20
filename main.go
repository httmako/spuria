package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Port              int
	IP                string
	IPwhitelist       bool
	WhitelistedIPs    map[string]bool
	CsvPath           string
	LogPath           string
	StaticCommand     string
	NoResult          bool
	RateLimit         int
	NoReplace         bool
	ReplaceRegex      *regexp.Regexp
	DontStopReplacing bool
	Timeout           int
	Verbose           bool
}

func LoadRoutesIntoMap(newMap map[string]string, csvText []byte) {
	r := csv.NewReader(bytes.NewReader(csvText))
	rows, err := r.ReadAll()
	if err != nil {
		panic(fmt.Errorf("erorr parsing csv: %s", err))
	}

	for k, row := range rows {
		if row[0] == "" || row[1] == "" {
			panic(fmt.Errorf("error parsing csv: line %d: wrong number of fields", k))
		}
		newMap[row[0]] = row[1]
	}
}

func parseFlags() (config *Config) {
	var conf Config
	var allowedIPs string
	var regex string
	flag.IntVar(&conf.Port, "port", 4870, "port to listen on")
	flag.StringVar(&conf.IP, "ip", "127.0.0.1", "which ip to listen on")
	flag.StringVar(&allowedIPs, "allowedips", "127.0.0.1", "which ips to respond to in a comma-sep list, e.g. `1.1.1.1,3.3.3.3` (set to \"\" to disable)")
	flag.StringVar(&conf.CsvPath, "routes", "", "bash commands file to load, e.g. `./routes.csv`")
	flag.StringVar(&conf.LogPath, "log", "stdout", "where to log to, e.g. `./spuria.log`")
	flag.StringVar(&conf.StaticCommand, "cmd", "", "static command to execute for /do , e.g. `\"echo 'hi'\"` , if this is set no csv (-routes) will be loaded")
	flag.BoolVar(&conf.NoResult, "noresult", false, "does not return command output in the http response, if true it will instead return OK/ERR for 200/500 responses")
	flag.IntVar(&conf.RateLimit, "maxratelimit", 10, "requests allowed per URL per minute, 0 = infinite")
	flag.BoolVar(&conf.NoReplace, "noreplace", false, "if true $variables in GET params and POST body will not be used in command execution")
	flag.StringVar(&regex, "replaceregex", "^[ a-zA-Z0-9/-]*$", "regex for allowed parameter replacing characters")
	flag.BoolVar(&conf.DontStopReplacing, "nostop", false, "do not stop when encountering an error in the parameter replacement")
	flag.IntVar(&conf.Timeout, "timeout", 30, "request (bash) timeout in seconds")
	flag.BoolVar(&conf.Verbose, "verbose", false, "log bash command output")

	flag.Parse()

	conf.WhitelistedIPs = map[string]bool{}
	if allowedIPs != "" {
		conf.IPwhitelist = true
		for _, ip := range strings.Split(allowedIPs, ",") {
			conf.WhitelistedIPs[strings.TrimSpace(ip)] = true
		}
	}
	var err error
	conf.ReplaceRegex, err = regexp.Compile(regex)
	if err != nil {
		panic(fmt.Errorf("error compiling regex: %s", err))
	}

	return &conf
}

func main() {
	starttime := time.Now()

	config := parseFlags()

	//log
	var logger *slog.Logger
	if config.LogPath == "stdout" {
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	} else {
		f, err := os.OpenFile(config.LogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			panic(fmt.Errorf("error opening log file: %s", err))
		}
		defer f.Close()
		logger = slog.New(slog.NewTextHandler(f, nil))
	}

	//routes / bash commands
	funcMap := map[string]string{}
	if config.StaticCommand != "" {
		funcMap["/do"] = config.StaticCommand
	} else if config.CsvPath != "" {
		fileBytes, err := os.ReadFile(config.CsvPath)
		if err != nil {
			panic(fmt.Errorf("error reading csv: %s", err))
		}
		LoadRoutesIntoMap(funcMap, fileBytes)
	} else {
		panic("ERROR: Please provide either -routes or -cmd !")
	}

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", config.IP, config.Port),
		Handler:      NewServer(config, funcMap, logger),
		WriteTimeout: time.Duration(config.Timeout) * time.Second,
	}

	logger.Info("Startup finished", "timetaken", time.Since(starttime), "ip", config.IP, "port", config.Port, "configLocation", config.CsvPath, "allowedIPs", config.WhitelistedIPs, "logLocation", config.LogPath)
	if err := httpServer.ListenAndServe(); err != nil {
		panic(err)
	}
}

func NewServer(config *Config, funcMap map[string]string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	//ratelimit
	mu := sync.Mutex{}
	// _=mu
	reqCounter := map[string]int{}
	// _=reqCounter
	resetTime := time.Now().Add(60 * time.Second).Unix()

	//web
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "# TYPE spuria_up counter\nspuria_up 1")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			io.WriteString(w, http.StatusText(http.StatusMethodNotAllowed))
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			panic(err)
		}

		//ip whitelist
		if exists, value := config.WhitelistedIPs[ip]; config.IPwhitelist && (!exists || !value) {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "NOACCESS")
			return
		}

		//ratelimiter
		mu.Lock()
		reqCounter[r.URL.Path]++
		if time.Now().Unix() > resetTime {
			reqCounter[r.URL.Path] = 1
			resetTime = time.Now().Add(60 * time.Second).Unix()
		}
		if reqCounter[r.URL.Path] > config.RateLimit && config.RateLimit != 0 {
			mu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		mu.Unlock()

		value, exists := funcMap[r.URL.Path]
		if !exists {
			http.NotFound(w, r)
			return
		}

		stdouterr, err := ExecuteCommand(r, value, config, logger)
		status := http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
		}
		w.WriteHeader(status)
		if !config.NoResult {
			fmt.Fprint(w, stdouterr)
		} else {
			io.WriteString(w, http.StatusText(status))
		}
	})
	return WrapLogging(mux, logger)
}

type logResponseWriter struct {
	http.ResponseWriter
	rc int
}

func (r *logResponseWriter) WriteHeader(statusCode int) {
	r.ResponseWriter.WriteHeader(statusCode)
	r.rc = statusCode
}
func WrapLogging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := logResponseWriter{ResponseWriter: w}
		defer func() {
			re := recover()
			logger.Info("webreq", "ip", r.RemoteAddr, "url", r.URL, "duration", time.Since(start), "status", lrw.rc, "err", re)
		}()
		next.ServeHTTP(&lrw, r)
	})
}

func ExecuteCommand(r *http.Request, command string, config *Config, logger *slog.Logger) (string, error) {
	path := r.URL.Path
	params := r.URL.Query()
	if len(params) > 0 && !config.NoReplace {
		for name, values := range params {
			value := values[0]
			if len(values) > 1 || !strings.HasPrefix(name, "$") || !config.ReplaceRegex.MatchString(value) {
				logger.Warn("replaceparam error", "path", path, "name", name, "value", value)
				if config.DontStopReplacing {
					continue
				} else {
					return "replaceparam error", fmt.Errorf("replaceparam error")
				}
			}
			command = strings.ReplaceAll(command, name, value)
		}
	}
	ec := exec.CommandContext(r.Context(), "bash", "-c", command) //.Output()
	starttime := time.Now()
	if r.Method == "POST" && !config.NoReplace {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			panic(fmt.Errorf("error reading body: %s", err))
		}
		stdin, err := ec.StdinPipe()
		if err != nil {
			panic(fmt.Errorf("error opening stdin: %s", err))
		}
		go func() {
			defer stdin.Close()
			stdin.Write(body)
		}()
	}
	body, err := ec.CombinedOutput()
	if config.Verbose {
		logger.Info("execution", "url", path, "command", command, "duration", time.Since(starttime), "stdouterr", string(body), "err", err)
	}
	return string(body), err
}
