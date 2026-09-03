package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/runner"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func newConfiguredRemoteWorker(
	catalog *config.Catalog,
	database *store.Store,
	stateRoot string,
) (*runner.RemoteWorker, error) {
	if catalog == nil || catalog.Fleet == nil || catalog.Fleet.RemoteWorker == nil ||
		!catalog.Fleet.RemoteWorker.Enabled {
		return nil, nil
	}
	policy := catalog.Fleet.RemoteWorker
	client, err := newRemoteWorkerHTTPClient(policy)
	if err != nil {
		return nil, err
	}
	privateKey, err := loadRemoteWorkerHeartbeatKey(catalog, policy)
	if err != nil {
		return nil, err
	}
	return &runner.RemoteWorker{
		RunnerID: policy.RunnerID, Endpoint: policy.ControlPlaneURL, Client: client,
		Catalog: catalog, Store: database, Executor: runner.CommandExecutor{}, StateRoot: stateRoot,
		HeartbeatPrivateKey: privateKey,
		Lease:               time.Duration(catalog.RunnerUpdate.LeaseSeconds) * time.Second,
		PollInterval:        time.Duration(policy.PollIntervalSeconds) * time.Second,
		HeartbeatInterval:   time.Duration(policy.HeartbeatIntervalSeconds) * time.Second,
	}, nil
}

func newRemoteWorkerHTTPClient(policy *config.RemoteWorkerPolicy) (*http.Client, error) {
	certificate, err := tls.LoadX509KeyPair(policy.MTLSCertificateFile, policy.MTLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("加载远程 Runner mTLS 客户端证书失败: %w", err)
	}
	if err := verifyRemoteWorkerCertificateIdentity(certificate, policy.RunnerID); err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(policy.ControlPlaneCAFile)
	if err != nil {
		return nil, fmt.Errorf("读取远程 Runner 控制面 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("远程 Runner 控制面 CA 没有有效证书")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, RootCAs: pool},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{Transport: transport}, nil
}

func verifyRemoteWorkerCertificateIdentity(certificate tls.Certificate, runnerID string) error {
	if len(certificate.Certificate) == 0 {
		return errors.New("远程 Runner mTLS 客户端证书链为空")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return fmt.Errorf("解析远程 Runner mTLS 客户端证书失败: %w", err)
	}
	matched := leaf.Subject.CommonName == runnerID
	for _, name := range leaf.DNSNames {
		matched = matched || name == runnerID
	}
	for _, uri := range leaf.URIs {
		matched = matched || uri.String() == runnerID || strings.HasSuffix(uri.String(), "/"+runnerID)
	}
	if !matched {
		return errors.New("远程 Runner mTLS 客户端证书未声明本地 Runner ID")
	}
	return nil
}

func loadRemoteWorkerHeartbeatKey(
	catalog *config.Catalog,
	policy *config.RemoteWorkerPolicy,
) (ed25519.PrivateKey, error) {
	encoded, err := os.ReadFile(policy.HeartbeatPrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("读取远程 Runner 心跳私钥失败: %w", err)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(string(bytes.TrimSpace(encoded)))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("远程 Runner 心跳私钥必须是 base64 编码的 Ed25519 私钥")
	}
	privateKey := ed25519.PrivateKey(decoded)
	expected, err := remoteWorkerHeartbeatPublicKey(catalog, policy.RunnerID)
	if err != nil {
		return nil, err
	}
	actual := privateKey.Public().(ed25519.PublicKey)
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return nil, errors.New("远程 Runner 心跳私钥与 Fleet 登记公钥不匹配")
	}
	return privateKey, nil
}

func remoteWorkerHeartbeatPublicKey(catalog *config.Catalog, runnerID string) (ed25519.PublicKey, error) {
	encoded := catalog.Fleet.RunnerPublicKeys[runnerID]
	if encoded == "" {
		for _, node := range catalog.Fleet.Inventory.Runners {
			if node.ID == runnerID {
				encoded = node.HeartbeatPublicKey
				break
			}
		}
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("Fleet 未登记远程 Runner 心跳公钥")
	}
	return ed25519.PublicKey(decoded), nil
}
