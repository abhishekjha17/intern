package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/abhishekjha17/intern/internal/logger"
	"github.com/abhishekjha17/intern/internal/profiler"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

const (
	upstreamURL = "https://api.anthropic.com"
	chanBufSize = 64
)

func main() {
	// Handle --version / -v before subcommand dispatch.
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Printf("intern %s (commit: %s, built: %s)\n", Version, Commit, Date)
			return
		}
		// Stop scanning at first non-flag argument.
		if len(arg) == 0 || arg[0] != '-' {
			break
		}
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "profile":
			os.Exit(runProfile(os.Args[2:]))
		case "proxy":
			os.Exit(runProxy(os.Args[2:]))
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	// Default: run proxy with all args (backward compatible).
	os.Exit(runProxy(os.Args[1:]))
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `intern %s — Anthropic Claude API proxy & profiler

Usage:
  intern [flags]              Start the proxy server (default)
  intern proxy [flags]        Start the proxy server
  intern profile [flags] <trace-files...>
                              Analyze trace files
  intern --version            Print version information

Run 'intern <command> --help' for details on a specific command.
`, Version)
}

// defaultTraceDir returns a trace file path under ~/.intern/traces/.
// Creates the directory if it doesn't exist.
func defaultTraceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "intern_traces.jsonl"
	}
	dir := filepath.Join(home, ".intern", "traces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("warning: could not create trace directory %s: %v", dir, err)
		return "intern_traces.jsonl"
	}
	return filepath.Join(dir, "traces.jsonl")
}

func runProxy(args []string) int {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	port := fs.Int("port", 11411, "port to listen on")
	traceFile := fs.String("trace", defaultTraceDir(), "path to the JSONL trace file")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: intern proxy [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	upstream, err := url.Parse(upstreamURL)
	if err != nil {
		log.Fatalf("invalid upstream URL: %v", err)
	}

	lt := logger.New(*traceFile, chanBufSize)
	defer lt.Close()

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = lt

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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Println("shutting down (draining in-flight requests)...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	log.Printf("intern proxy listening on %s → %s (traces → %s)", addr, upstreamURL, *traceFile)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
	return 0
}

func runProfile(args []string) int {
	fs := flag.NewFlagSet("profile", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "output as JSON")
	pricingPath := fs.String("pricing", "", "path to a JSON pricing override file (defaults to $XDG_CONFIG_HOME/intern/pricing.json if present)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: intern profile [flags] <trace-files...>\n\nFlags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	files := fs.Args()
	if len(files) == 0 {
		fs.Usage()
		return 1
	}

	if source, err := profiler.LoadPricing(*pricingPath); err != nil {
		fmt.Fprintf(os.Stderr, "error loading pricing: %v\n", err)
		return 1
	} else if source != "embedded" {
		fmt.Fprintf(os.Stderr, "pricing: loaded overrides from %s\n", source)
	}

	traces, err := profiler.LoadTraces(files)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if len(traces) == 0 {
		fmt.Fprintln(os.Stderr, "no traces found in the provided files")
		return 1
	}

	report := profiler.Analyze(traces, files)

	if *jsonOutput {
		if err := profiler.RenderJSON(os.Stdout, report); err != nil {
			fmt.Fprintf(os.Stderr, "error writing JSON: %v\n", err)
			return 1
		}
	} else {
		profiler.RenderText(os.Stdout, report)
	}

	return 0
}
