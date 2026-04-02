package main

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"

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

func envOr(key, def string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return def
}

var listenPort = flag.String("port", envOr("SRV_PORT", "8080"),
	"Port to listen for connections on.")

func main() {
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
			log.Info("heartbeat", zap.Int("count", n))
			n++
		}
	}(log)

	var requestCount atomic.Int64
	err = http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	if err != nil {
		log.Fatal("server exited with error", zap.Error(err))
	}
}
