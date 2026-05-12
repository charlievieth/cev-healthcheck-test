package main

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/gorilla/handlers"
	"go.uber.org/zap"
)

func instanceID() (string, error) {
	var buf [64]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return "", err
	}
	d := sha256.Sum256(buf[:])
	return hex.EncodeToString(d[:6]), nil
}

// loggedHeaders are the headers that we log in production
var loggedHeaders = [...]struct {
	canonical string
	lower     string
}{
	{"User-Agent", "user-agent"},
	{"X-Forwarded-For", "x-forwarded-for"}, // Originating IP address OR "Cf-Connecting-Ip"
	{"Cf-Ray", "cf-ray"},                   // Cloudflare unique identifier header
}

type zapLogFormatter struct {
	log *zap.Logger
}

// LoggingHandler wraps the inner http.Handler to provide request logging.
func LoggingHandler(inner http.Handler, log *zap.Logger) http.Handler {
	h := &zapLogFormatter{
		// Disable reporting the caller since it's always the middleware
		// and adding a skip just means it'll report the caller as being
		// in the net/http stack.
		log: log,
	}
	return handlers.CustomLoggingHandler(nil, inner, h.Format)
}

// Format function for github.com/gorilla/handlers.CustomLoggingHandler
func (z *zapLogFormatter) Format(_ io.Writer, params handlers.LogFormatterParams) {
	// The below was copied from:
	// https://github.com/gorilla/handlers/blob/v1.5.2/logging.go#L147-L189

	url := params.URL
	username := "-"
	if url.User != nil {
		if name := url.User.Username(); name != "" {
			username = name
		}
	}

	req := params.Request
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}

	uri := req.RequestURI

	// Requests using the CONNECT method over HTTP/2.0 must use
	// the authority field (aka r.Host) to identify the target.
	// Refer: https://httpwg.github.io/specs/rfc7540.html#CONNECT
	if req.ProtoMajor == 2 && req.Method == "CONNECT" {
		uri = req.Host
	}
	if uri == "" {
		uri = url.RequestURI()
	}

	hdrs := make([]zap.Field, 0, len(loggedHeaders))
	for _, h := range loggedHeaders {
		if v := req.Header.Get(h.canonical); v != "" {
			hdrs = append(hdrs, zap.String(h.lower, v))
		}
	}

	z.log.Info(
		"handled http request",
		zap.String("host", host), // Remote address
		zap.String("username", username),
		zap.String("method", req.Method),
		zap.String("uri", uri),
		zap.String("proto", req.Proto),
		zap.Dict("headers", hdrs...),
		zap.Int("status_code", params.StatusCode),
		zap.Int("size", params.Size),
	)
}

func envOr(key, def string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return def
}

var listenPort = flag.String("port", envOr("SRV_PORT", "8080"),
	"Port to listen for connections on.")

func main() {
	flag.Parse()
	id, err := instanceID()
	if err != nil {
		panic(err)
	}

	log, err := zap.NewProduction(zap.AddStacktrace(zap.FatalLevel))
	if err != nil {
		panic(err)
	}
	log = log.With(zap.String("instance_id", id))

	addr := "0.0.0.0:" + *listenPort
	log.Info("starting http server", zap.String("address", addr))

	// Add a heartbeat for the sake of seeing logs.
	go func(log *zap.Logger) {
		n := 0
		tick := time.NewTicker(time.Second * 10)
		for range tick.C {
			log.Info("heartbeat test", zap.Int("count", n))
			n++
		}
	}(log)

	var requestCount atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if r.Response != nil && r.Response.Body != nil {
				_, _ = io.Copy(io.Discard, r.Response.Body)
			}
		}()

		url := "<none>"
		if r.URL != nil {
			url = r.URL.String()
		}
		log := log.With(
			zap.Int64("request_id", requestCount.Add(1)),
			zap.String("method", r.Method),
			zap.String("url", url),
			zap.String("remote_addr", r.RemoteAddr),
		)
		log.Info("received request")

		data, err := json.Marshal(map[string]string{
			"method":      r.Method,
			"url":         url,
			"remote_addr": r.RemoteAddr,
		})
		if err != nil {
			log.Error("failed to marshal response", zap.Error(err))
			http.Error(w, fmt.Sprintf("error: %s", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(200)
		w.Write(data)
	})
	if err := http.ListenAndServe(addr, LoggingHandler(handler, log)); err != nil {
		log.Fatal("server exited with error", zap.Error(err))
	}
}
