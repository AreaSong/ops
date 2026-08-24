package runner

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (server *Server) metrics(response http.ResponseWriter, request *http.Request) {
	metrics, err := server.store.CollectMetrics(request.Context())
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "指标读取失败")
		return
	}
	var output strings.Builder
	output.WriteString("# HELP areasong_ops_build_info 控制面组件构建身份。\n")
	output.WriteString("# TYPE areasong_ops_build_info gauge\n")
	fmt.Fprintf(&output, "areasong_ops_build_info{component=%q,version=%q,revision=%q} 1\n",
		"runner", buildinfo.Version, buildinfo.Revision)
	output.WriteString("# HELP areasong_ops_runner_up Runner 健康状态。\n")
	output.WriteString("# TYPE areasong_ops_runner_up gauge\nareasong_ops_runner_up 1\n")
	output.WriteString("# HELP areasong_ops_tasks_total 按状态统计的任务数量。\n")
	output.WriteString("# TYPE areasong_ops_tasks_total gauge\n")
	states := []model.TaskState{
		model.TaskQueued, model.TaskRunning, model.TaskSucceeded, model.TaskFailed,
		model.TaskFailedRecoverable, model.TaskNeedsAttention, model.TaskRolledBack, model.TaskRecoveryUncertain,
	}
	output.WriteString("# HELP areasong_ops_runner_heartbeat_timestamp_seconds Runner 最近心跳时间。\n")
	output.WriteString("# TYPE areasong_ops_runner_heartbeat_timestamp_seconds gauge\n")
	fmt.Fprintf(&output, "areasong_ops_runner_heartbeat_timestamp_seconds %.0f\n", float64(time.Now().Unix()))
	output.WriteString("# HELP areasong_ops_desired_actual_drift 服务目标状态与实际状态不一致。\n")
	output.WriteString("# TYPE areasong_ops_desired_actual_drift gauge\n")
	for _, state := range server.engine.ServiceStates(request.Context()) {
		drift := 0
		if state.Drift != nil && state.Drift.Detected {
			drift = 1
		}
		fmt.Fprintf(&output, "areasong_ops_desired_actual_drift{service=%q,desired=%q,actual=%q} %d\n", state.Service, state.Desired, state.Actual, drift)
	}
	for _, state := range states {
		fmt.Fprintf(&output, "areasong_ops_tasks_total{state=%q} %d\n", state, metrics.TasksByState[state])
	}
	output.WriteString("# HELP areasong_ops_service_tasks_total 按服务、动作和状态统计的任务数量。\n")
	output.WriteString("# TYPE areasong_ops_service_tasks_total gauge\n")
	for _, item := range metrics.TasksByService {
		fmt.Fprintf(&output, "areasong_ops_service_tasks_total{service=%q,action=%q,state=%q} %d\n",
			item.Service, item.Action, item.State, item.Count)
	}
	output.WriteString("# HELP areasong_ops_last_task_finished_timestamp_seconds 最近终态任务完成时间。\n")
	output.WriteString("# TYPE areasong_ops_last_task_finished_timestamp_seconds gauge\n")
	for _, item := range metrics.LastFinishedTasks {
		fmt.Fprintf(&output, "areasong_ops_last_task_finished_timestamp_seconds{service=%q,action=%q,state=%q} %.0f\n",
			item.Service, item.Action, item.State, item.FinishedEpoch)
	}
	output.WriteString("# HELP areasong_ops_oldest_active_task_age_seconds 最老活动任务的持续时间。\n")
	output.WriteString("# TYPE areasong_ops_oldest_active_task_age_seconds gauge\n")
	fmt.Fprintf(&output, "areasong_ops_oldest_active_task_age_seconds %.0f\n", metrics.OldestActiveAge)
	output.WriteString("# HELP areasong_ops_last_sqlite_snapshot_timestamp_seconds 最近 SQLite 快照时间。\n")
	output.WriteString("# TYPE areasong_ops_last_sqlite_snapshot_timestamp_seconds gauge\n")
	fmt.Fprintf(&output, "areasong_ops_last_sqlite_snapshot_timestamp_seconds %.0f\n", metrics.LastSnapshotEpoch)
	output.WriteString("# HELP areasong_ops_credential_rotation_active 当前活动凭据轮换状态。\n")
	output.WriteString("# TYPE areasong_ops_credential_rotation_active gauge\n")
	for _, item := range metrics.ActiveCredentialRotations {
		fmt.Fprintf(&output, "areasong_ops_credential_rotation_active{credential_type=%q,state=%q} 1\n",
			item.CredentialType, item.State)
	}
	output.WriteString("# HELP areasong_ops_credential_rotation_age_seconds 当前活动凭据轮换状态持续时间。\n")
	output.WriteString("# TYPE areasong_ops_credential_rotation_age_seconds gauge\n")
	for _, item := range metrics.ActiveCredentialRotations {
		fmt.Fprintf(&output, "areasong_ops_credential_rotation_age_seconds{credential_type=%q,state=%q} %.0f\n",
			item.CredentialType, item.State, item.AgeSeconds)
	}
	output.WriteString("# HELP areasong_ops_service_action_enabled 服务能力是否开放。\n")
	output.WriteString("# TYPE areasong_ops_service_action_enabled gauge\n")
	names := server.engine.catalog.ServiceNames()
	for _, name := range names {
		actionNames := make([]string, 0, len(server.engine.catalog.Services[name].Actions))
		for action := range server.engine.catalog.Services[name].Actions {
			actionNames = append(actionNames, action)
		}
		sort.Strings(actionNames)
		for _, actionName := range actionNames {
			enabled := 0
			if server.engine.catalog.Services[name].Actions[actionName].Enabled {
				enabled = 1
			}
			fmt.Fprintf(&output, "areasong_ops_service_action_enabled{service=%q,action=%q} %d\n", name, actionName, enabled)
		}
	}
	output.WriteString("# HELP areasong_ops_object_action_enabled 受管对象能力是否开放。\n")
	output.WriteString("# TYPE areasong_ops_object_action_enabled gauge\n")
	for _, name := range server.engine.catalog.ObjectNames() {
		object, _ := server.engine.catalog.Object(name)
		actionNames := make([]string, 0, len(object.Actions))
		for action := range object.Actions {
			actionNames = append(actionNames, action)
		}
		sort.Strings(actionNames)
		for _, actionName := range actionNames {
			enabled := 0
			if object.Actions[actionName].Enabled {
				enabled = 1
			}
			fmt.Fprintf(&output, "areasong_ops_object_action_enabled{object_id=%q,object_type=%q,action=%q} %d\n",
				object.ObjectID, object.Metadata.Type, actionName, enabled)
		}
	}
	response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte(output.String()))
}
