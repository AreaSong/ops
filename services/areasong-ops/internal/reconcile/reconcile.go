// Package reconcile contains deterministic state interpretation and matching
// logic. It deliberately has no I/O or clock access so callers can replay an
// observation exactly.
package reconcile

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const defaultDriftReason = "目标状态与实际状态不一致"

// Reconcile interprets an untyped observer payload and compares it with the
// requested lifecycle state. Missing or malformed values remain unknown; the
// function never panics and never mutates observation.
func Reconcile(observation map[string]any, desired model.DesiredState, now time.Time) (model.ServiceState, *model.StateDrift) {
	if observation == nil {
		observation = map[string]any{}
	}

	state := model.ServiceState{
		Service:    stringField(observation, "service", "serviceName"),
		ObjectID:   stringField(observation, "objectId", "objectID", "object"),
		TenantID:   stringField(observation, "tenantId", "tenantID", "tenant"),
		Desired:    desired,
		Actual:     actualState(observation),
		Health:     healthState(observation),
		ObservedAt: observedTime(observation, now),
		Reason:     stringField(observation, "reason", "message", "summary", "error"),
		Data:       observationData(observation),
	}

	matched := stateMatches(desired, state.Actual, state.Health)
	if matched {
		return state, nil
	}

	reason := state.Reason
	if reason == "" {
		reason = defaultDriftReason
	}
	detectedAt := state.ObservedAt
	if detectedAt.IsZero() {
		detectedAt = now
	}
	drift := &model.StateDrift{
		Detected:   true,
		Expected:   string(desired),
		Observed:   string(state.Actual),
		Reason:     reason,
		DetectedAt: detectedAt,
	}
	state.Drift = drift
	return state, drift
}

func stateMatches(desired model.DesiredState, actual model.ActualState, health model.HealthState) bool {
	switch desired {
	case model.DesiredRunning:
		return actual == model.ActualRunning && health == model.HealthHealthy
	case model.DesiredStopped:
		return actual == model.ActualStopped
	case model.DesiredMaintenance:
		return actual == model.ActualMaintenance
	case model.DesiredDrained:
		return actual == model.ActualDrained
	default:
		return false
	}
}

func actualState(values map[string]any) model.ActualState {
	// Public traffic is an independent state dimension. A stopped application
	// wins over a maintenance barrier because the lifecycle contract models
	// the terminal application state as stopped; maintenance/drained otherwise
	// wins over an otherwise-running application.
	application := model.ActualUnknown
	for _, key := range []string{"appState", "applicationState", "containerState", "runtimeState"} {
		if value, ok := lookup(values, key); ok {
			if parsed := parseActual(value); parsed != model.ActualUnknown {
				application = parsed
				break
			}
		}
	}
	if value, ok := lookup(values, "trafficState"); ok {
		traffic := parseActual(value)
		if application == model.ActualStopped {
			return application
		}
		switch traffic {
		case model.ActualMaintenance, model.ActualDrained, model.ActualStopped:
			return traffic
		}
	}
	for _, key := range []string{"actual", "actualState", "lifecycleState", "state", "status"} {
		if value, ok := lookup(values, key); ok {
			if parsed := parseActual(value); parsed != model.ActualUnknown {
				return parsed
			}
		}
	}
	if application != model.ActualUnknown {
		return application
	}
	return model.ActualUnknown
}

func parseActual(value any) model.ActualState {
	if nested, ok := stringMap(value); ok {
		for _, key := range []string{"actual", "actualState", "state", "status"} {
			if candidate, found := lookup(nested, key); found {
				return parseActual(candidate)
			}
		}
		return model.ActualUnknown
	}
	value = normalizeToken(value)
	switch value {
	case "running", "started", "start", "up", "online", "active", "ready":
		return model.ActualRunning
	case "stopped", "stop", "exited", "exit", "down", "dead", "terminated", "offline":
		return model.ActualStopped
	case "maintenance", "maint":
		return model.ActualMaintenance
	case "drained", "drain", "draining":
		return model.ActualDrained
	default:
		return model.ActualUnknown
	}
}

func healthState(values map[string]any) model.HealthState {
	for _, key := range []string{"health", "healthState", "healthStatus", "status"} {
		if value, ok := lookup(values, key); ok {
			if parsed := parseHealth(value); parsed != model.HealthUnknown {
				return parsed
			}
		}
	}

	// Adapters expose dependency health as individual state fields. A single
	// failed dependency makes the aggregate unhealthy; partial degradation is
	// retained when at least one dependency is degraded.
	seenDegraded := false
	seenHealth := false
	for key, value := range values {
		canonical := canonicalKey(key)
		if !strings.HasSuffix(canonical, "state") || canonical == "appstate" || canonical == "actualstate" {
			continue
		}
		parsed := parseHealth(value)
		if parsed == model.HealthUnknown {
			parsed = healthFromState(value)
		}
		if parsed == model.HealthUnknown {
			continue
		}
		seenHealth = true
		if parsed == model.HealthUnhealthy {
			return model.HealthUnhealthy
		}
		if parsed == model.HealthDegraded {
			seenDegraded = true
		}
	}
	if seenDegraded {
		return model.HealthDegraded
	}
	if seenHealth {
		return model.HealthHealthy
	}
	return model.HealthUnknown
}

func parseHealth(value any) model.HealthState {
	if nested, ok := stringMap(value); ok {
		for _, key := range []string{"health", "healthState", "status", "state", "result"} {
			if candidate, found := lookup(nested, key); found {
				if parsed := parseHealth(candidate); parsed != model.HealthUnknown {
					return parsed
				}
			}
		}
		for _, key := range []string{"ok", "healthy", "ready", "passing"} {
			if candidate, found := lookup(nested, key); found {
				if boolValue, ok := candidate.(bool); ok {
					if boolValue {
						return model.HealthHealthy
					}
					return model.HealthUnhealthy
				}
			}
		}
		return model.HealthUnknown
	}
	if boolValue, ok := value.(bool); ok {
		if boolValue {
			return model.HealthHealthy
		}
		return model.HealthUnhealthy
	}
	switch normalizeToken(value) {
	case "healthy", "health", "ok", "passing", "pass", "ready", "up":
		return model.HealthHealthy
	case "degraded", "degrade", "warning", "warn", "stale", "partial":
		return model.HealthDegraded
	case "unhealthy", "failed", "failure", "fail", "critical", "down", "error", "unavailable":
		return model.HealthUnhealthy
	default:
		return model.HealthUnknown
	}
}

func healthFromState(value any) model.HealthState {
	switch parseActual(value) {
	case model.ActualRunning:
		return model.HealthHealthy
	case model.ActualStopped:
		return model.HealthUnhealthy
	default:
		return model.HealthUnknown
	}
}

func observedTime(values map[string]any, fallback time.Time) time.Time {
	for _, key := range []string{"observedAt", "observationTime", "timestamp", "time"} {
		if value, ok := lookup(values, key); ok {
			if parsed, valid := parseTime(value); valid {
				return parsed
			}
		}
	}
	return fallback
}

func parseTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, !typed.IsZero()
	case *time.Time:
		if typed == nil || typed.IsZero() {
			return time.Time{}, false
		}
		return *typed, true
	case json.Number:
		return unixTime(typed.String())
	case float64:
		return unixTime(strconv.FormatFloat(typed, 'f', -1, 64))
	case float32:
		return unixTime(strconv.FormatFloat(float64(typed), 'f', -1, 32))
	case int:
		return time.Unix(int64(typed), 0).UTC(), true
	case int8:
		return time.Unix(int64(typed), 0).UTC(), true
	case int16:
		return time.Unix(int64(typed), 0).UTC(), true
	case int32:
		return time.Unix(int64(typed), 0).UTC(), true
	case int64:
		return time.Unix(typed, 0).UTC(), true
	case uint:
		return time.Unix(int64(typed), 0).UTC(), true
	case uint8:
		return time.Unix(int64(typed), 0).UTC(), true
	case uint16:
		return time.Unix(int64(typed), 0).UTC(), true
	case uint32:
		return time.Unix(int64(typed), 0).UTC(), true
	case uint64:
		if typed > math.MaxInt64 {
			return time.Time{}, false
		}
		return time.Unix(int64(typed), 0).UTC(), true
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed, true
		}
		return unixTime(value)
	default:
		return time.Time{}, false
	}
}

func unixTime(value string) (time.Time, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	// JSON observers occasionally report milliseconds since epoch.
	if seconds >= 1e12 {
		seconds /= 1e3
	}
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * 1e9)
	return time.Unix(whole, nanos).UTC(), true
}

func observationData(values map[string]any) map[string]any {
	if nested, ok := lookup(values, "data"); ok {
		if data, ok := stringMap(nested); ok {
			return cloneMap(data)
		}
	}
	return cloneMap(values)
}

func stringField(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := lookup(values, key); ok {
			if text := scalarString(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case model.DesiredState:
		return string(typed)
	case model.ActualState:
		return string(typed)
	case model.HealthState:
		return string(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	case json.Number:
		return typed.String()
	case fmtStringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

// fmtStringer keeps scalarString free from fmt's broad, map-producing
// formatting behaviour while still accepting common Stringer values.
type fmtStringer interface{ String() string }

func normalizeToken(value any) string {
	return strings.ToLower(strings.TrimSpace(scalarString(value)))
}

func lookup(values map[string]any, wanted string) (any, bool) {
	if value, ok := values[wanted]; ok {
		return value, true
	}
	wanted = canonicalKey(wanted)
	for key, value := range values {
		if canonicalKey(key) == wanted {
			return value, true
		}
	}
	return nil, false
}

func canonicalKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "", "-", "", " ", "").Replace(value)
	return value
}

func stringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = item
		}
		return converted, true
	default:
		return nil, false
	}
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	if nested, ok := stringMap(value); ok {
		return cloneMap(nested)
	}
	switch typed := value.(type) {
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		// A reflect fallback handles slices decoded into a concrete numeric
		// type without imposing a dependency on a serialization package.
		reflected := reflect.ValueOf(value)
		if reflected.IsValid() && reflected.Kind() == reflect.Slice {
			cloned := reflect.MakeSlice(reflected.Type(), reflected.Len(), reflected.Len())
			reflect.Copy(cloned, reflected)
			return cloned.Interface()
		}
		return value
	}
}
