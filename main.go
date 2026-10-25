// Command token-server is the VoltKit RTC token server.
//
//	go run .                 # start on $HOST:$PORT (default 0.0.0.0:7000)
//	go run . --check-config  # validate .env and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	loadDotEnv(".env")
	cfg, err := readConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "--check-config" {
		fmt.Printf("Configuration valid, port %d.\n", cfg.Port)
		return
	}

	rtc := NewVoltKitClient(cfg.BaseURL, cfg.APIKey, 8*time.Second)
	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:           newHandler(cfg.AppKey, rtc),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// Stop cleanly on SIGTERM/SIGINT so in-flight requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("VoltKit token server on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
