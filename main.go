package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/abhishekjha17/intern/internal/logger"
)

const (
	upstreamURL = "https://api.anthropic.com"
	chanBufSize = 64
)

func main() {
	port := flag.Int("port", 8080, "port to listen on")
	traceFile := flag.String("trace", "intern_traces.jsonl", "path to the JSONL trace file")
	flag.Parse()

	upstream, err := url.Parse(upstreamURL)
	if err != nil {
		log.Fatalf("invalid upstream URL: %v", err)
	}

	lt := logger.New(*traceFile, chanBufSize)
	defer lt.Close()

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = lt

	// Rewrite the Host header so api.anthropic.com sees itself as the target.
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = upstream.Scheme
		req.URL.Host = upstream.Host
		req.Host = upstream.Host
	}

	addr := fmt.Sprintf(":%d", *port)
	srv := &http.Server{
		Addr:    addr,
		Handler: proxy,
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Println("shutting down...")
		if err := srv.Close(); err != nil {
			log.Printf("server close error: %v", err)
		}
	}()

	log.Printf("intern proxy listening on %s → %s (traces → %s)", addr, upstreamURL, *traceFile)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
