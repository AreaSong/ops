package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var autoUpdateWindowPattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]-(?:[01][0-9]|2[0-3]):[0-5][0-9](?:Z)?$`)

func validateAutoUpdatePolicy(service string, policy *model.AutoUpdatePolicy) error {
	if policy == nil {
		return nil
	}
	if policy.Channel == "" {
		policy.Channel = "stable"
	}
	if policy.Channel != "stable" && policy.Channel != "candidate" && policy.Channel != "security" {
		return fmt.Errorf("服务 %s 的自动更新 channel 无效", service)
	}
	if policy.MaintenanceWindow != "" && !autoUpdateWindowPattern.MatchString(policy.MaintenanceWindow) {
		return fmt.Errorf("服务 %s 的自动更新维护窗口必须为 HH:MM-HH:MM", service)
	}
	policy.MaintenanceTimezone = strings.TrimSpace(policy.MaintenanceTimezone)
	if policy.MaintenanceTimezone == "" {
		policy.MaintenanceTimezone = "UTC"
	}
	if _, err := time.LoadLocation(policy.MaintenanceTimezone); err != nil {
		return fmt.Errorf("服务 %s 的自动更新维护窗口时区无效", service)
	}
	if policy.CanaryPercent < 0 || policy.CanaryPercent > 100 {
		return fmt.Errorf("服务 %s 的自动更新 canary 百分比无效", service)
	}
	if policy.MaxUnavailable < 0 || policy.MaxUnavailable > 100 {
		return fmt.Errorf("服务 %s 的自动更新并发上限无效", service)
	}
	if policy.ObservationSeconds == 0 {
		policy.ObservationSeconds = 300
	}
	if policy.ObservationSeconds < 60 || policy.ObservationSeconds > 24*60*60 {
		return fmt.Errorf("服务 %s 的自动更新观察窗口必须为 60 到 86400 秒", service)
	}
	// An enabled production policy can prepare a plan automatically, but a
	// human approval remains mandatory. This prevents a catalog typo from
	// turning a scheduler into an unreviewed deployment channel.
	if policy.Enabled && (!policy.RequireApproval || !policy.RequireBackup || !policy.RollbackOnAlert) {
		return fmt.Errorf("服务 %s 的自动更新必须同时启用人工批准、新鲜备份和告警回滚门禁", service)
	}
	return nil
}
