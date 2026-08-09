package web

import (
	"context"
	"net"
	"net/http"
	"time"
)

type RunnerClient struct {
	normal *http.Client
	stream *http.Client
}

func NewRunnerClient(socketPath string) *RunnerClient {
	transport := func() *http.Transport {
		return &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
			DisableCompression: true,
			MaxIdleConns:       16,
			IdleConnTimeout:    90 * time.Second,
		}
	}
	return &RunnerClient{
		normal: &http.Client{Transport: transport(), Timeout: 45 * time.Second},
		stream: &http.Client{Transport: transport()},
	}
}
