package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

type RunnerClient struct {
	normal     *http.Client
	credential *http.Client
	stream     *http.Client
}

func NewRunnerClient(socketPath string) *RunnerClient {
	return newRunnerClient("unix", socketPath)
}

// NewRemoteRunnerClient intentionally refuses non-Unix endpoints. The Web
// process is a local control-plane client and must never drift onto the remote
// Runner mTLS listener, which exposes only signed heartbeat traffic.
func NewRemoteRunnerClient(_ string) (*RunnerClient, error) {
	return nil, errors.New("Web Runner 客户端仅允许 Unix Socket")
}

func newRunnerClient(network, address string) *RunnerClient {
	transport := func() *http.Transport {
		return &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, address)
			},
			DisableCompression: true,
			MaxIdleConns:       16,
			IdleConnTimeout:    90 * time.Second,
		}
	}
	return &RunnerClient{
		normal:     &http.Client{Transport: transport(), Timeout: 45 * time.Second},
		credential: &http.Client{Transport: transport(), Timeout: 95 * time.Second},
		stream:     &http.Client{Transport: transport()},
	}
}
