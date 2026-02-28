// Package config provides env-overridable constants for the proxy.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

var (
	UpstreamTimeout    = envDuration("UPSTREAM_TIMEOUT_SECS", 30*time.Second)
	MaxBodyBytes       = envInt64("MAX_BODY_BYTES", 10<<20) // 10 MB
	MaxHeaderBytes     = envInt64("MAX_HEADER_BYTES", 64<<10) // 64 KB
	ClientRecvTimeout  = envDuration("CLIENT_RECV_TIMEOUT_SECS", 5*time.Minute)
	MaxProxyGoroutines = envInt("MAX_PROXY_GOROUTINES", 128)

	MitmCacheMaxSize      = envInt("MITM_CACHE_MAX_SIZE", 256)
	MitmCertValidityHours = envFloat("MITM_CERT_VALIDITY_HOURS", 1.0)
)

func envInt(name string, def int) int {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		panic(fmt.Sprintf("%s must be a positive integer, got %q", name, s))
	}
	return v
}

func envInt64(name string, def int64) int64 {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 1 {
		panic(fmt.Sprintf("%s must be a positive integer, got %q", name, s))
	}
	return v
}

func envFloat(name string, def float64) float64 {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		panic(fmt.Sprintf("%s must be a positive number, got %q", name, s))
	}
	return v
}

func envDuration(name string, def time.Duration) time.Duration {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	secs, err := strconv.Atoi(s)
	if err != nil || secs < 1 {
		panic(fmt.Sprintf("%s must be a positive integer (seconds), got %q", name, s))
	}
	return time.Duration(secs) * time.Second
}
