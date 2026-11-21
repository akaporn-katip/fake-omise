// Command fake-omise serves a stand-in for the Omise payment API.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akaporn-katip/fake-omise/internal/api"
	"github.com/akaporn-katip/fake-omise/internal/store"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	dbPath := flag.String("db", "fake-omise.db", "path to the SQLite database")
	threeDS := flag.String("3ds", api.ThreeDSAlways, "when to require 3-D Secure: always, zero_amount or off")
	webhookURL := flag.String("webhook", "", "URL to deliver event webhooks to (optional)")
	baseURL := flag.String("base-url", "", "public base URL used in authorize_uri (optional)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *threeDS != api.ThreeDSAlways && *threeDS != api.ThreeDSZeroAmount && *threeDS != api.ThreeDSOff {
		log.Fatalf("invalid -3ds value %q: want always, zero_amount or off", *threeDS)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer st.Close()

	logger := log.New(os.Stderr, "fake-omise ", log.LstdFlags)
	server := api.New(st, api.Config{
		ThreeDS:    *threeDS,
		WebhookURL: *webhookURL,
		BaseURL:    *baseURL,
		Logger:     logger,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Printf("%s listening on %s (db=%s, 3ds=%s, webhook=%s)", version, *addr, *dbPath, *threeDS, orNone(*webhookURL))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Print("shutting down")
	_ = srv.Close()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
