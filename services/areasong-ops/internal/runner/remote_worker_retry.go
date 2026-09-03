package runner

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

const maxRemoteWorkerRetryInterval = 30 * time.Second

type remoteWorkerHTTPError struct {
	operation  string
	status     string
	statusCode int
}

func (failure remoteWorkerHTTPError) Error() string {
	return fmt.Sprintf("%s失败: %s", failure.operation, failure.status)
}

func newRemoteWorkerHTTPError(operation string, response *http.Response) error {
	return remoteWorkerHTTPError{
		operation: operation, status: response.Status, statusCode: response.StatusCode,
	}
}

func isRetryableRemoteWorkerError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var httpFailure remoteWorkerHTTPError
	if errors.As(err, &httpFailure) {
		return httpFailure.statusCode == http.StatusRequestTimeout ||
			httpFailure.statusCode == http.StatusTooEarly ||
			httpFailure.statusCode == http.StatusTooManyRequests ||
			httpFailure.statusCode >= http.StatusInternalServerError
	}
	// 证书信任或主机名错误属于身份配置故障，重试不能修复，必须立即暴露。
	var hostnameError x509.HostnameError
	var authorityError x509.UnknownAuthorityError
	var certificateError x509.CertificateInvalidError
	if errors.As(err, &hostnameError) || errors.As(err, &authorityError) || errors.As(err, &certificateError) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	var urlError *url.Error
	return errors.As(err, &urlError)
}

func remoteWorkerRetryDelay(attempt int, base time.Duration) time.Duration {
	if base <= 0 {
		base = defaultWorkerPollInterval
	}
	delay := base
	for current := 0; current < attempt && delay < maxRemoteWorkerRetryInterval; current++ {
		if delay > maxRemoteWorkerRetryInterval/2 {
			return maxRemoteWorkerRetryInterval
		}
		delay *= 2
	}
	if delay > maxRemoteWorkerRetryInterval {
		return maxRemoteWorkerRetryInterval
	}
	return delay
}

func (worker *RemoteWorker) establishIdentityHeartbeat(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		err := worker.sendIdentityHeartbeat(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		if !isRetryableRemoteWorkerError(err) {
			return err
		}
		delay := remoteWorkerRetryDelay(attempt, worker.PollInterval)
		logRemoteWorkerRetry("初始身份心跳", err, delay)
		if stopped, _ := worker.waitForInterval(ctx, nil, delay); stopped {
			return nil
		}
	}
}

func (worker *RemoteWorker) waitForInterval(
	ctx context.Context,
	heartbeatErrors <-chan error,
	delay time.Duration,
) (bool, error) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true, nil
	case err := <-heartbeatErrors:
		return false, err
	case <-timer.C:
		return false, nil
	}
}

func logRemoteWorkerRetry(operation string, err error, delay time.Duration) {
	slog.Warn("远程 Runner 临时通信失败，将退避重试",
		"operation", operation, "error", err, "retry_in", delay)
}
