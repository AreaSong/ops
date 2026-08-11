package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const alertmanagerResponseLimit = 2 << 20

var alertFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{8,64}$`)

type Alertmanager interface {
	ListAlerts(context.Context, bool) ([]model.ActiveAlert, error)
	CreateSilence(context.Context, map[string]string, []string, time.Time, time.Time, string) (model.MaintenanceSilence, error)
	ExpireSilence(context.Context, string) error
}

type AlertmanagerClient struct {
	baseURL *url.URL
	client  *http.Client
}

func NewAlertmanagerClient(rawURL string) (*AlertmanagerClient, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("Alertmanager 地址必须是本机 HTTP origin")
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return nil, errors.New("Alertmanager 地址必须指向本机回环地址")
	}
	return &AlertmanagerClient{
		baseURL: parsed,
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (client *AlertmanagerClient) ListAlerts(
	ctx context.Context,
	includeSilenced bool,
) ([]model.ActiveAlert, error) {
	endpoint := client.resolve("/api/v2/alerts")
	query := endpoint.Query()
	query.Set("active", "true")
	query.Set("silenced", fmt.Sprintf("%t", includeSilenced))
	query.Set("inhibited", "false")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("读取 Alertmanager 活动告警失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取 Alertmanager 活动告警失败: HTTP %d", response.StatusCode)
	}
	var payload []struct {
		Annotations map[string]string `json:"annotations"`
		Labels      map[string]string `json:"labels"`
		StartsAt    time.Time         `json:"startsAt"`
		Fingerprint string            `json:"fingerprint"`
		Status      struct {
			SilencedBy []string `json:"silencedBy"`
		} `json:"status"`
	}
	if err := decodeAlertmanagerJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("解析 Alertmanager 活动告警失败: %w", err)
	}
	alerts := make([]model.ActiveAlert, 0, len(payload))
	for _, item := range payload {
		if !alertFingerprintPattern.MatchString(item.Fingerprint) || item.Labels["alertname"] == "" {
			return nil, errors.New("Alertmanager 返回了无效告警身份")
		}
		alerts = append(alerts, model.ActiveAlert{
			Fingerprint: item.Fingerprint,
			AlertName:   item.Labels["alertname"],
			Severity:    item.Labels["severity"],
			Summary:     item.Annotations["summary"],
			RunbookURL:  safeHTTPSURL(item.Annotations["runbook_url"]),
			GrafanaURL:  safeHTTPSURL(item.Annotations["grafana_url"]),
			Labels:      item.Labels,
			Silenced:    len(item.Status.SilencedBy) > 0,
			StartsAt:    item.StartsAt,
		})
	}
	return alerts, nil
}

func (client *AlertmanagerClient) CreateSilence(
	ctx context.Context,
	matchers map[string]string,
	alertNames []string,
	startsAt, endsAt time.Time,
	comment string,
) (model.MaintenanceSilence, error) {
	if len(matchers) == 0 || len(alertNames) == 0 || !endsAt.After(startsAt) ||
		endsAt.Sub(startsAt) > 4*time.Hour {
		return model.MaintenanceSilence{}, errors.New("维护静默范围或时长无效")
	}
	type matcher struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		IsRegex bool   `json:"isRegex"`
		IsEqual bool   `json:"isEqual"`
	}
	keys := make([]string, 0, len(matchers))
	for name := range matchers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	payloadMatchers := make([]matcher, 0, len(matchers)+1)
	for _, name := range keys {
		payloadMatchers = append(payloadMatchers, matcher{
			Name: name, Value: matchers[name], IsEqual: true,
		})
	}
	expressions := make([]string, 0, len(alertNames))
	for _, name := range alertNames {
		expressions = append(expressions, regexp.QuoteMeta(name))
	}
	sort.Strings(expressions)
	payloadMatchers = append(payloadMatchers, matcher{
		Name: "alertname", Value: "^(?:" + strings.Join(expressions, "|") + ")$",
		IsRegex: true, IsEqual: true,
	})
	payload := struct {
		Matchers  []matcher `json:"matchers"`
		StartsAt  time.Time `json:"startsAt"`
		EndsAt    time.Time `json:"endsAt"`
		CreatedBy string    `json:"createdBy"`
		Comment   string    `json:"comment"`
	}{payloadMatchers, startsAt, endsAt, "areasong-ops", comment}
	body, err := json.Marshal(payload)
	if err != nil {
		return model.MaintenanceSilence{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.resolve("/api/v2/silences").String(), strings.NewReader(string(body)))
	if err != nil {
		return model.MaintenanceSilence{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return model.MaintenanceSilence{}, fmt.Errorf("创建 Alertmanager 维护静默失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return model.MaintenanceSilence{}, fmt.Errorf("创建 Alertmanager 维护静默失败: HTTP %d", response.StatusCode)
	}
	var result struct {
		ID string `json:"silenceID"`
	}
	if err := decodeAlertmanagerJSON(response.Body, &result); err != nil || result.ID == "" {
		return model.MaintenanceSilence{}, errors.New("Alertmanager 未返回有效静默标识")
	}
	return model.MaintenanceSilence{ID: result.ID, EndsAt: endsAt}, nil
}

func (client *AlertmanagerClient) ExpireSilence(ctx context.Context, id string) error {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return errors.New("Alertmanager 静默标识无效")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		client.resolve("/api/v2/silence/"+url.PathEscape(id)).String(), nil)
	if err != nil {
		return err
	}
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("解除 Alertmanager 维护静默失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("解除 Alertmanager 维护静默失败: HTTP %d", response.StatusCode)
	}
	return nil
}

func (client *AlertmanagerClient) resolve(path string) *url.URL {
	result := *client.baseURL
	result.Path = path
	result.RawQuery = ""
	return &result
}

func decodeAlertmanagerJSON(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, alertmanagerResponseLimit+1))
	if err != nil {
		return err
	}
	if len(data) > alertmanagerResponseLimit {
		return errors.New("响应超过大小限制")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("响应包含多余 JSON 数据")
	}
	return nil
}

func safeHTTPSURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return parsed.String()
}
