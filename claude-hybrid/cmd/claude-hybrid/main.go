// claude-hybrid launches a MITM routing proxy and runs claude through it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/proxy"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: claude-hybrid [proxy-flags] [-- claude-flags]

Starts a local MITM routing proxy and launches Claude Code through it.
Arguments after -- are passed directly to claude.

Examples:
  claude-hybrid
  claude-hybrid --verbose
  claude-hybrid -- --dangerously-skip-permissions
  claude-hybrid --verbose -- --dangerously-skip-permissions

Proxy flags:
`)
		flag.PrintDefaults()
	}
	port := flag.Int("port", 0, "proxy listen port (0 = random)")
	bind := flag.String("bind", "127.0.0.1", "proxy bind address")
	certsDir := flag.String("certs-dir", defaultCertsDir(), "directory for CA cert/key")
	proxyOnly := flag.Bool("proxy-only", false, "run proxy without launching claude")
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	flag.Parse()

	// Ensure base directory exists
	baseDir := filepath.Dir(*certsDir)
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "create base dir: %v\n", err)
		os.Exit(1)
	}

	// Open log file, truncating if from a previous day
	logPath := filepath.Join(baseDir, "proxy.log")
	logFlags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if shouldTruncateLog(logPath) {
		logFlags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	logFile, err := os.OpenFile(logPath, logFlags, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()
	log.SetOutput(logFile)

	// Ensure certs directory exists
	if err := os.MkdirAll(*certsDir, 0700); err != nil {
		log.Fatalf("create certs dir: %v", err)
	}

	certPath := filepath.Join(*certsDir, "ca.crt")
	keyPath := filepath.Join(*certsDir, "ca.key")

	// Generate CA if needed
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		log.Println("Generating MITM CA certificate...")
		certPEM, keyPEM, err := mitm.GenerateCA()
		if err != nil {
			log.Fatalf("generate CA: %v", err)
		}
		if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
			log.Fatalf("write CA cert: %v", err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
			log.Fatalf("write CA key: %v", err)
		}
		log.Printf("CA certificate written to %s", certPath)
	}

	// Load CA
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		log.Fatalf("read CA cert: %v", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		log.Fatalf("read CA key: %v", err)
	}

	certCache, err := mitm.NewCertCache(certPEM, keyPEM)
	if err != nil {
		log.Fatalf("create cert cache: %v", err)
	}

	// Load provider config (optional)
	opts := []proxy.Option{proxy.WithVerbose(*verbose)}
	cfgPath := filepath.Join(baseDir, "config.yaml")
	if _, err := os.Stat(cfgPath); err == nil {
		cfg, err := config.LoadConfig(cfgPath)
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		resolver, err := config.NewModelResolver(cfg)
		if err != nil {
			log.Fatalf("build model resolver: %v", err)
		}
		opts = append(opts, proxy.WithModelResolver(resolver))
		log.Printf("Loaded provider config from %s", cfgPath)
	} else {
		log.Printf("No config at %s — local routes will return stub responses", cfgPath)
	}

	// Start proxy
	p := proxy.New(certCache, opts...)
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *bind, *port))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	proxyAddr := ln.Addr().String()
	log.Printf("Proxy listening on %s", proxyAddr)

	srv := &http.Server{Handler: p}
	go srv.Serve(ln)

	if *proxyOnly {
		log.Println("Running in proxy-only mode (Ctrl+C to stop)")
		// Block forever (until signal kills us)
		select {}
	}

	// Launch claude with proxy env vars
	claudeArgs := flag.Args()
	cmd := exec.Command("claude", claudeArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"HTTPS_PROXY=http://"+proxyAddr,
		"NODE_EXTRA_CA_CERTS="+certPath,
	)

	if err := cmd.Run(); err != nil {
		srv.Close()
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Fatalf("claude: %v", err)
	}
	srv.Close()
}

// shouldTruncateLog returns true if the log file was last modified before today.
func shouldTruncateLog(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false // file doesn't exist, will be created fresh
	}
	now := time.Now()
	modTime := info.ModTime()
	return modTime.Year() != now.Year() || modTime.YearDay() != now.YearDay()
}

func defaultCertsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude-hybrid/certs"
	}
	return filepath.Join(home, ".claude-hybrid", "certs")
}
