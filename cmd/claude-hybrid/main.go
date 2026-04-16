// claude-hybrid launches a MITM routing proxy and runs claude through it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/proxy"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "trust" {
		os.Exit(runTrust(os.Args[2:]))
	}

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

	// Open log file with daily rotation. Use an exclusive lock for
	// truncation to prevent races between concurrent instances.
	logPath := filepath.Join(baseDir, "proxy.log")
	if shouldTruncateLog(logPath) {
		tryTruncateLog(logPath)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()
	sessionID := fmt.Sprintf("s%d", os.Getpid())
	log.SetOutput(logFile)
	log.SetPrefix(fmt.Sprintf("[%s] ", sessionID))

	// Ensure certs directory exists
	if err := os.MkdirAll(*certsDir, 0700); err != nil {
		log.Fatalf("create certs dir: %v", err)
	}

	certPath, _, certPEM, keyPEM, err := ensureCA(*certsDir, log.Printf)
	if err != nil {
		log.Fatalf("ensure CA: %v", err)
	}

	// Load CA
	certCache, err := mitm.NewCertCache(certPEM, keyPEM)
	if err != nil {
		log.Fatalf("create cert cache: %v", err)
	}
	if !systemTrustInstalled() {
		log.Printf("CA is not installed in system trust store; run 'claude-hybrid trust install' for Python/pip compatibility")
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
		"NODE_USE_SYSTEM_CA=1",
		"SSL_CERT_FILE="+systemCertBundlePath(),
		"REQUESTS_CA_BUNDLE="+systemCertBundlePath(),
		"CURL_CA_BUNDLE="+systemCertBundlePath(),
		"PIP_CERT="+systemCertBundlePath(),
	)

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}

	if err := cmd.Run(); err != nil {
		shutdown()
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Fatalf("claude: %v", err)
	}
	shutdown()
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

// tryTruncateLog truncates the log file while holding an exclusive lock,
// preventing races between concurrent instances.
func tryTruncateLog(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	// Try non-blocking exclusive lock — if another instance holds it, skip truncation
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	// Re-check after acquiring lock (another instance may have already truncated)
	info, err := f.Stat()
	if err != nil {
		return
	}
	now := time.Now()
	modTime := info.ModTime()
	if modTime.Year() != now.Year() || modTime.YearDay() != now.YearDay() {
		f.Truncate(0)
	}
}

func defaultCertsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude-hybrid/certs"
	}
	return filepath.Join(home, ".claude-hybrid", "certs")
}

func ensureCA(certsDir string, logf func(string, ...interface{})) (certPath, keyPath string, certPEM, keyPEM []byte, err error) {
	certPath = filepath.Join(certsDir, "ca.crt")
	keyPath = filepath.Join(certsDir, "ca.key")

	if _, statErr := os.Stat(certPath); os.IsNotExist(statErr) {
		lockPath := filepath.Join(certsDir, "ca.lock")
		lockFile, lockErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if lockErr != nil {
			if logf != nil {
				logf("Waiting for another instance to generate CA certificate...")
			}
			for i := 0; i < 50; i++ {
				time.Sleep(100 * time.Millisecond)
				if _, err := os.Stat(certPath); err == nil {
					break
				}
			}
			if _, err := os.Stat(certPath); os.IsNotExist(err) {
				return "", "", nil, nil, fmt.Errorf("timed out waiting for CA certificate generation")
			}
		} else {
			lockFile.Close()
			defer os.Remove(lockPath)
			if logf != nil {
				logf("Generating MITM CA certificate...")
			}
			certPEM, keyPEM, err = mitm.GenerateCA()
			if err != nil {
				return "", "", nil, nil, err
			}
			if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
				return "", "", nil, nil, err
			}
			if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
				return "", "", nil, nil, err
			}
			if logf != nil {
				logf("CA certificate written to %s", certPath)
			}
		}
	}

	if certPEM == nil {
		certPEM, err = os.ReadFile(certPath)
		if err != nil {
			return "", "", nil, nil, err
		}
	}
	if keyPEM == nil {
		keyPEM, err = os.ReadFile(keyPath)
		if err != nil {
			return "", "", nil, nil, err
		}
	}
	return certPath, keyPath, certPEM, keyPEM, nil
}

func runTrust(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: claude-hybrid trust <install|status>")
		return 2
	}
	certsDir := defaultCertsDir()
	if err := os.MkdirAll(certsDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "create cert dir: %v\n", err)
		return 1
	}
	certPath, _, _, _, err := ensureCA(certsDir, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ensure CA: %v\n", err)
		return 1
	}

	switch args[0] {
	case "install":
		if err := installTrust(certPath); err != nil {
			fmt.Fprintf(os.Stderr, "install trust: %v\n", err)
			return 1
		}
		fmt.Printf("Installed %s into system trust store.\n", certPath)
		fmt.Printf("Use SSL_CERT_FILE=%s for tools that ignore system trust.\n", systemCertBundlePath())
		return 0
	case "status":
		installed := systemTrustInstalled()
		fmt.Printf("CA path: %s\n", certPath)
		fmt.Printf("System trust: %v\n", installed)
		if installed {
			return 0
		}
		return 1
	default:
		fmt.Fprintf(os.Stderr, "unknown trust subcommand %q\n", args[0])
		return 2
	}
}

func installTrust(certPath string) error {
	cmds := [][]string{{"trust", "anchor", certPath}, {"update-ca-trust"}}
	for _, argv := range cmds {
		if err := runElevated(argv...); err != nil {
			return err
		}
	}
	return nil
}

func runElevated(argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if os.Geteuid() != 0 {
		argv = append([]string{"sudo"}, argv...)
		cmd = exec.Command(argv[0], argv[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
	}
	return cmd.Run()
}

func systemTrustInstalled() bool {
	out, err := exec.Command("trust", "list").CombinedOutput()
	if err != nil {
		return false
	}
	return containsIgnoreCase(string(out), "claude-hybrid MITM CA")
}

func containsIgnoreCase(s, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	return len(s) >= len(needle) && containsFold(s, needle)
}

func containsFold(s, needle string) bool {
	for i := 0; i+len(needle) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca := a[i]
		cb := b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func systemCertBundlePath() string {
	return "/etc/ssl/certs/ca-certificates.crt"
}
