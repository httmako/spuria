package main

import (
	"bytes"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	ReturnResult      bool
	RateLimit         int
	ReplaceParam      bool
	ReplaceRegex      *regexp.Regexp
	DontStopReplacing bool
}

func LoadRoutesIntoMap(newMap *sync.Map, csvText []byte, logger *slog.Logger) {
	r := csv.NewReader(bytes.NewReader(csvText))
	rows, err := r.ReadAll()
	if err != nil {
		panic(fmt.Errorf("erorr parsing csv: %s",err))
	}

	for k, row := range rows {
		path := row[0]
		if path == "" {
			logger.Warn("Skipping row because of missing URL", "row", k+1)
			continue
		}
		cmd := row[1]
		if cmd == "" {
			logger.Warn("Skipping row because of missing command", "row", k+1)
			continue
		}
		newMap.Store(path, cmd)
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
	flag.BoolVar(&conf.ReturnResult, "returnresult", false, "returns the command output in the http response, default is OK/ERR for 200/500 response body")
	flag.IntVar(&conf.RateLimit, "maxratelimit", 10, "requests allowed per URL per minute, 0 = infinite")
	flag.BoolVar(&conf.ReplaceParam, "replaceparam", false, "replace GET parameters starting with $ inside the bash script, POST body will be replacing $body")
	flag.StringVar(&regex, "replaceregex", "^[ a-zA-Z0-9/-]*$", "regex for allowed parameter replacing characters")
	flag.BoolVar(&conf.DontStopReplacing, "nostop", false, "do not stop when encountering an error in the parameter replacement")

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
		panic(fmt.Errorf("error compiling regex: %s",err))
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
			fmt.Println("ERROR opening log file")
			panic(err)
		}
		defer f.Close()
		logger = slog.New(slog.NewTextHandler(f, nil))
	}

	//routes / bash commands
	funcMap := sync.Map{}
	if config.StaticCommand != "" {
		funcMap.Store("/do", config.StaticCommand)
	} else if config.CsvPath != "" {
		fileBytes, err := os.ReadFile(config.CsvPath)
		if err != nil {
			panic(fmt.Errorf("error reading csv: %s",err))
		}
		LoadRoutesIntoMap(&funcMap, fileBytes, logger)
	} else {
		panic("ERROR: Please provide either -routes or -cmd !")
	}

	httpServer := &http.Server{
		Addr:    net.JoinHostPort(config.IP, strconv.Itoa(config.Port)),
		Handler: NewServer(config, &funcMap, logger),
	}

	logger.Info("Startup finished", "timetaken", time.Since(starttime), "ip", config.IP, "port", config.Port, "configLocation", config.CsvPath, "allowedIPs", config.WhitelistedIPs, "logLocation", config.LogPath)
	if err := httpServer.ListenAndServe(); err != nil {
		panic(err)
	}
}

func NewServer(config *Config, funcMap *sync.Map, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	//ratelimit
	mu := sync.Mutex{}
	// _=mu
	reqCounter := map[string]int{}
	// _=reqCounter
	resetTime := atomic.Int64{}
	resetTime.Store(time.Now().Add(60 * time.Second).Unix())

	//web
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "# TYPE isupdummy counter")
		fmt.Fprintln(w, "isupdummy 1")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, http.StatusText(http.StatusMethodNotAllowed))
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			panic(err)
		}

		//ip whitelist
		if exists, value := config.WhitelistedIPs[ip]; config.IPwhitelist && (!exists || !value) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "NOACCESS")
			return
		}

		//ratelimiter
		mu.Lock()
		reqCounter[r.URL.Path]++
		timeWhenReset := resetTime.Load()
		if time.Now().Unix() > timeWhenReset {
			reqCounter[r.URL.Path] = 1
			resetTime.Store(time.Now().Add(60 * time.Second).Unix())
		}
		if reqCounter[r.URL.Path] > config.RateLimit && config.RateLimit != 0 {
			mu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		mu.Unlock()

		value, exists := funcMap.Load(r.URL.Path)
		if !exists {
			http.NotFound(w, r)
			return
		}

		err, stdout, stderr := ExecuteCommand(r, value.(string), config, logger)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			if config.ReturnResult {
				fmt.Fprint(w, stderr)
			} else {
				fmt.Fprint(w, "ERR")
			}
		} else {
			w.WriteHeader(http.StatusOK)
			if config.ReturnResult {
				fmt.Fprint(w, stdout)
			} else {
				fmt.Fprint(w, "OK")
			}
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

func ExecuteCommand(r *http.Request, command string, config *Config, logger *slog.Logger) (error, string, string) {
	path := r.URL.Path
	params := r.URL.Query()
	if r.Method == "POST" {
		params = map[string][]string{}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			panic(fmt.Errorf("error reading body: %s",err))
		}
		params["$body"] = []string{string(body)}
	}
	if len(params) > 0 && config.ReplaceParam {
		for name, values := range params {
			value := values[0]
			if len(values) <= 0 || len(values) > 1 {
				logger.Warn("get param error, please only set GET parameter value once for each key", "path", path, "name", name, "length", len(values))
				if config.DontStopReplacing {
					continue
				} else {
					return errors.New("GET param has more than 1 or less than 1 values"), "", ""
				}
			}

			if !strings.HasPrefix(name, "$") {
				logger.Warn("get param error, name has to begin with $", "path", path, "name", name, "value", value)
				if config.DontStopReplacing {
					continue
				} else {
					return errors.New("GET param name doesn't begin with $"), "", ""
				}
			}
			if !config.ReplaceRegex.MatchString(value) {
				logger.Warn("get param error, invalid input for regex", "path", path, "name", name, "value", value)
				if config.DontStopReplacing {
					continue
				} else {
					return errors.New("GET param value doesn't match regex"), "", ""
				}
			}
			command = strings.ReplaceAll(command, name, value)
		}
	}
	ec := exec.Command("bash", "-c", command) //.Output()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	ec.Stdout = &stdout
	ec.Stderr = &stderr
	starttime := time.Now()
	err := ec.Run()
	logger.Info("execution", "path", path, "duration", time.Since(starttime), "stdout", stdout.String(), "stderr", stderr.String(), "err", err)
	return err, stdout.String(), stderr.String()
}
