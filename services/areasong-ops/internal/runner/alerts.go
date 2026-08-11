package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type unavailableAlertmanager struct{}

func (unavailableAlertmanager) ListAlerts(context.Context, bool) ([]model.ActiveAlert, error) {
	return nil, errors.New("Alertmanager 客户端未配置")
}

func (unavailableAlertmanager) CreateSilence(
	context.Context,
	map[string]string,
	[]string,
	time.Time,
	time.Time,
	string,
) (model.MaintenanceSilence, error) {
	return model.MaintenanceSilence{}, errors.New("Alertmanager 客户端未配置")
}

func (unavailableAlertmanager) ExpireSilence(context.Context, string) error {
	return errors.New("Alertmanager 客户端未配置")
}

func (engine *Engine) ActiveAlerts(ctx context.Context) ([]model.ActiveAlert, error) {
	alerts, err := engine.alertmanager.ListAlerts(ctx, false)
	if err != nil {
		return nil, err
	}
	return engine.projectAlerts(alerts, ""), nil
}

func (engine *Engine) blockingAlerts(
	ctx context.Context,
	service model.ServiceDefinition,
) ([]model.ActiveAlert, error) {
	alerts, err := engine.alertmanager.ListAlerts(ctx, true)
	if err != nil {
		return nil, err
	}
	return engine.projectAlerts(alerts, service.Name), nil
}

func (engine *Engine) projectAlerts(alerts []model.ActiveAlert, serviceName string) []model.ActiveAlert {
	result := make([]model.ActiveAlert, 0)
	seen := make(map[string]struct{})
	for _, service := range engine.catalog.Services {
		if serviceName != "" && service.Name != serviceName {
			continue
		}
		blocking := stringSet(service.AlertPolicy.BlockingAlerts)
		for _, alert := range alerts {
			if _, ok := blocking[alert.AlertName]; !ok || !labelsMatch(alert.Labels, service.AlertPolicy.Matchers) {
				continue
			}
			if _, duplicate := seen[alert.Fingerprint]; duplicate {
				continue
			}
			seen[alert.Fingerprint] = struct{}{}
			alert.ObjectID = service.ObjectID
			alert.Service = service.Name
			result = append(result, alert)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		leftSeverity := alertSeverityRank(result[left].Severity)
		rightSeverity := alertSeverityRank(result[right].Severity)
		if leftSeverity != rightSeverity {
			return leftSeverity < rightSeverity
		}
		if !result[left].StartsAt.Equal(result[right].StartsAt) {
			return result[left].StartsAt.Before(result[right].StartsAt)
		}
		return result[left].Fingerprint < result[right].Fingerprint
	})
	return result
}

func alertSeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func (engine *Engine) prepareMaintenanceSilence(
	ctx context.Context,
	plan model.ReleasePlan,
	service model.ServiceDefinition,
	action model.ActionDefinition,
) (*model.MaintenanceSilence, error) {
	if !model.ActionRequiresObservation(action) ||
		(action.Risk != model.RiskMedium && action.Risk != model.RiskHigh) {
		return nil, nil
	}
	blockers, err := engine.blockingAlerts(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("执行门禁无法读取 Alertmanager: %w", err)
	}
	if len(blockers) > 0 {
		return nil, fmt.Errorf("存在阻断告警，禁止开始生产变更: %s", alertNames(blockers))
	}
	if len(service.AlertPolicy.MaintenanceAlerts) == 0 {
		return nil, errors.New("生产变更缺少受控维护静默映射")
	}
	now := time.Now().UTC()
	duration := time.Duration(action.TimeoutSeconds+action.ObservationSeconds+600) * time.Second
	if duration > 4*time.Hour {
		duration = 4 * time.Hour
	}
	silence, err := engine.alertmanager.CreateSilence(ctx, service.AlertPolicy.Matchers,
		service.AlertPolicy.MaintenanceAlerts, now, now.Add(duration),
		"AreaSong Ops 受控计划 "+plan.ID+" · "+plan.Service+"/"+plan.Action)
	if err != nil {
		return nil, err
	}
	return &silence, nil
}

func labelsMatch(labels, matchers map[string]string) bool {
	for name, expected := range matchers {
		if labels[name] != expected {
			return false
		}
	}
	return true
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func alertNames(alerts []model.ActiveAlert) string {
	values := make([]string, 0, len(alerts))
	seen := make(map[string]struct{})
	for _, alert := range alerts {
		if _, exists := seen[alert.AlertName]; exists {
			continue
		}
		seen[alert.AlertName] = struct{}{}
		values = append(values, alert.AlertName)
		if len(values) == 3 {
			break
		}
	}
	return strings.Join(values, "、")
}

func alertFingerprints(alerts []model.ActiveAlert) []string {
	values := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		values = append(values, alert.Fingerprint)
	}
	sort.Strings(values)
	return values
}
