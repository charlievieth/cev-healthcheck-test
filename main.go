package main

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

func instanceID() (string, error) {
	var buf [32]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return "", err
	}
	d := sha256.Sum256(buf[:]) // digest
	fmt.Printf("%x\n", d)
	return hex.EncodeToString(d[:])[:8], nil
}

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

	log.Info("starting http server")

	go func() {
		n := 0
		tick := time.NewTicker(time.Second)
		for range tick.C {
			log.Info("heartbeat", zap.Int("count", n))
			n++
		}
	}()

	// Add a heartbeat for the sack of seeing logs

	// TODO: Add some per-request logging
	err = http.ListenAndServe("0.0.0.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Response != nil && r.Response.Body != nil {
			_, _ = io.Copy(io.Discard, r.Response.Body)
		}
		w.WriteHeader(200)
	}))
	if err != nil {
		log.Fatal("server exited with error", zap.Error(err))
	}
}
