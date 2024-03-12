package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func LoadConfig(path string, logger *slog.Logger) sync.Map {
	newMap := sync.Map{}
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		logger.Error("Couldn't read config.csv!")
		panic(err)
	}
	r := csv.NewReader(bytes.NewReader(fileBytes))
	rows, err := r.ReadAll()
	if err != nil {
		logger.Error("Error parsing config.csv!")
		panic(err)
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
	return newMap
}

func ParseIPList(input string) map[string]bool {
	newMap := map[string]bool{}
	list := strings.Split(input, ",")
	for _, ip := range list {
		newMap[strings.TrimSpace(ip)] = true
	}
	return newMap
}

var port = flag.Int("port", 4870, "port to listen on")
var ip = flag.String("ip", "127.0.0.1", "which ip to listen on")
var ipListStr = flag.String("allowed", "127.0.0.1", "which ips to respond to in a comma-sep list, e.g. `1.1.1.1,3.3.3.3`")
var configPath = flag.String("routes", "", "bash commands file to load, e.g. `./routes.csv`")
var logPath = flag.String("log", "stdout", "where to log to, e.g. `./spuria.log`")
var staticCommand = flag.String("cmd", "", "static command to execute for /do , e.g. `\"echo 'hi'\"` , if this is set no csv (-routes) will be loaded")
var returnResult = flag.Bool("verbose", false, "returns the command output in the http response, default is OK/ERR for 200/500 response body")
var rateLimit = flag.Int("ratelimit", 10, "requests allowed per URL per minute")
var replaceParam = flag.Bool("replaceparam", false, "replace GET parameters starting with $ inside the bash script")

func main() {
	flag.Parse()
	starttime := time.Now()

	//log
	var logger *slog.Logger
	if *logPath == "stdout" {
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	} else {
		f, err := os.OpenFile(*logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		defer f.Close()
		if err != nil {
			fmt.Println("ERROR opening log file")
			panic(err)
		}
		logger = slog.New(slog.NewTextHandler(f, nil))
	}

	//routes / bash commands
	funcMap := sync.Map{}
	if *staticCommand != "" {
		funcMap.Store("/do", *staticCommand)
	} else if *configPath != "" {
		funcMap = LoadConfig(*configPath, logger)
	} else {
		panic("ERROR: Please provide either -routes or -cmd !")
	}

	//ip whitelist
	allowIPActive := false
	allowedIPsMap := map[string]bool{}
	if *ipListStr != "" {
		allowIPActive = true
		allowedIPsMap = ParseIPList(*ipListStr)
	}

	//ratelimit
	mu := sync.Mutex{}
	// _=mu
	reqCounter := map[string]int{}
	// _=reqCounter
	resetTime := atomic.Int64{}
	resetTime.Store(time.Now().Add(60 * time.Second).Unix())

	//web
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "# TYPE isupdummy counter")
		fmt.Fprintln(w, "isupdummy 1")
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			LogRequest(logger, r, 200, nil)
			return
		}

		defer func() {
			if rc := recover(); rc != nil {
				err := rc.(error)
				LogRequest(logger, r, 500, err)
			}
		}()

		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			fmt.Println("ERROR WHEN PARSING REMOTEADDR")
			fmt.Println(err)
			return
		}

		//ip whitelist
		if exists, value := allowedIPsMap[ip]; allowIPActive && (!exists || !value) {
			fmt.Fprintf(w, "NOACCESS")
			LogRequest(logger, r, 403, nil)
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
		if reqCounter[r.URL.Path] > *rateLimit {
			mu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			LogRequest(logger, r, 429, nil)
			return
		}
		mu.Unlock()

		if value, exists := funcMap.Load(r.URL.Path); exists {
			err, stdout, stderr := ExecuteCommand(r, value.(string), logger)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				if *returnResult {
					fmt.Fprintf(w, stderr)
				} else {
					fmt.Fprintf(w, "ERR")
				}
				LogRequest(logger, r, 500, nil)
			} else {
				w.WriteHeader(http.StatusOK)
				if *returnResult {
					fmt.Fprintf(w, stdout)
				} else {
					fmt.Fprintf(w, "OK")
				}
				LogRequest(logger, r, 200, nil)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "URL not found or configured! (%q)", r.URL.Path)
		LogRequest(logger, r, 404, nil)
	})

	donetime := time.Now()
	logger.Info("Startup finished", "timetaken", donetime.Sub(starttime).String(), "ip", *ip, "port", *port, "configLocation", *configPath, "allowedIPs", *ipListStr, "logLocation", *logPath)
	fmt.Println("Listening on ", *ip, ":", *port)

	fmt.Println(http.ListenAndServe(*ip+":"+strconv.Itoa(*port), nil))
}

func LogRequest(logger *slog.Logger, r *http.Request, returnCode int, err error) {
	logger.Info("request", "method", r.Method, "url", r.URL.Path, "status", returnCode, "source", r.RemoteAddr, "proto", r.Proto, "host", r.Host, "referer", r.Referer(), "useragent", r.UserAgent(), "err", err)
}

func ExecuteCommand(r *http.Request, command string, logger *slog.Logger) (error, string, string) {
	path := r.URL.Path
	params := r.URL.Query()
	if len(params) > 0 && *replaceParam {
		for name, value := range params {
			if len(value) <= 0 {
				logger.Warn("get param error, no value for param?", "path", path, "name", name)
				continue
			}
			if len(value) > 1 {
				logger.Warn("get param error, please do not use params more than once", "path", path, "name", name)
				continue
			}
			firstValue := value[0]
			if !strings.HasPrefix(name, "$") {
				logger.Warn("get param error, name has to begin with $", "path", path, "name", name, "value", firstValue)
				continue
			}
			command = strings.ReplaceAll(command, name, firstValue)
		}
	}
	ec := exec.Command("bash", "-c", command) //.Output()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	ec.Stdout = &stdout
	ec.Stderr = &stderr
	starttime := time.Now()
	err := ec.Run()
	timeTaken := time.Now().Sub(starttime).String()
	outStr := stdout.String()
	errStr := stderr.String()
	if err != nil {
		logger.Error("execution error", "path", path, "duration", timeTaken, "stdout", outStr, "stderr", errStr, "err", err)
		fmt.Println(err)
		return err, outStr, errStr
	}
	logger.Info("execution success", "path", path, "duration", timeTaken, "stdout", outStr, "stderr", errStr)
	return nil, outStr, errStr
}
