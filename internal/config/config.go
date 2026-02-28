// Package config provides constants and configuration for the proxy.
package config

import "time"

const (
	UpstreamTimeout    = 30 * time.Second
	MaxBodyBytes       = 10 << 20 // 10 MB
	ClientRecvTimeout  = 5 * time.Minute
	MaxProxyGoroutines = 128

	MitmCacheMaxSize      = 256
	MitmCertValidityHours = 1.0
)
