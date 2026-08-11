package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const actorHeader = "X-AreaSong-Ops-Actor-Hash"

type Server struct {
	engine *Engine
	store  *store.Store
}

func NewServer(engine *Engine, database *store.Store) http.Handler {
	server := &Server{engine: engine, store: database}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /metrics", server.metrics)
	mux.HandleFunc("GET /v1/services", server.services)
	mux.HandleFunc("GET /v1/objects", server.objects)
	mux.HandleFunc("GET /v1/automatic-tasks", server.automaticTasks)
	mux.HandleFunc("GET /v1/alerts", server.alerts)
	mux.HandleFunc("POST /v1/previews", server.createPreview)
	mux.HandleFunc("POST /v1/plans", server.createPlan)
	mux.HandleFunc("GET /v1/plans", server.plans)
	mux.HandleFunc("GET /v1/plans/{id}", server.plan)
	mux.HandleFunc("POST /v1/plans/{id}/approve", server.approvePlan)
	mux.HandleFunc("POST /v1/plans/{id}/execute", server.executePlan)
	mux.HandleFunc("POST /v1/plans/{id}/close", server.closePlan)
	mux.HandleFunc("POST /v1/tasks", server.startTask)
	mux.HandleFunc("GET /v1/tasks", server.tasks)
	mux.HandleFunc("GET /v1/tasks/{id}", server.task)
	mux.HandleFunc("GET /v1/tasks/{id}/events", server.taskEvents)
	mux.HandleFunc("POST /v1/tasks/{id}/recovery", server.recoverTask)
	mux.HandleFunc("GET /v1/audit", server.audit)
	mux.HandleFunc("GET /v1/events", server.events)
	return requestLimits(mux)
}

func (server *Server) objects(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"objects": server.engine.Objects(request.Context())})
}

func (server *Server) automaticTasks(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"automaticTasks": server.engine.AutomaticTasks(request.Context())})
}

func (server *Server) alerts(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	alerts, err := server.engine.ActiveAlerts(request.Context())
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "Alertmanager 活动告警当前不可用")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"alerts": alerts})
}

func (server *Server) closePlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ClosePlanRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := server.engine.CloseReleasePlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (server *Server) recoverTask(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.RecoveryRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := server.engine.CreateRecoveryPlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, plan)
}

func (server *Server) createPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.PreviewRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := server.engine.CreateReleasePlan(request.Context(), actor, input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, plan)
}

func (server *Server) plans(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	limit := queryLimit(request, 50, 200)
	plans, err := server.store.ListReleasePlans(request.Context(), limit+1, queryOffset(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取发布计划失败")
		return
	}
	hasMore := len(plans) > limit
	if hasMore {
		plans = plans[:limit]
	}
	writeJSON(response, http.StatusOK, map[string]any{"plans": plans, "hasMore": hasMore})
}

func (server *Server) plan(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	plan, err := server.store.GetReleasePlan(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取发布计划失败")
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (server *Server) approvePlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ApprovePlanRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := server.engine.ApproveReleasePlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (server *Server) executePlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ExecutePlanRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	task, created, err := server.engine.ExecuteReleasePlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(response, status, task)
}

func requestLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Cache-Control", "no-store")
		if request.Body != nil {
			request.Body = http.MaxBytesReader(response, request.Body, 64<<10)
		}
		next.ServeHTTP(response, request)
	})
}

func (server *Server) health(response http.ResponseWriter, request *http.Request) {
	if _, err := server.store.LatestEventSequence(request.Context()); err != nil {
		writeError(response, http.StatusServiceUnavailable, "SQLite 不可用")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"ok": true, "component": "runner",
		"version": buildinfo.Version, "revision": buildinfo.Revision,
	})
}

func (server *Server) services(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"services": server.engine.Services(request.Context())})
}

func (server *Server) createPreview(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.PreviewRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	preview, err := server.engine.CreatePreview(request.Context(), actor, input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, preview)
}

func (server *Server) startTask(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.StartTaskRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	task, created, err := server.engine.StartTask(request.Context(), actor, input)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(response, status, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(response, status, task)
}

func (server *Server) tasks(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	limit := queryLimit(request, 50, 200)
	tasks, err := server.store.ListTasks(request.Context(), limit+1, queryOffset(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取任务失败")
		return
	}
	hasMore := len(tasks) > limit
	if hasMore {
		tasks = tasks[:limit]
	}
	writeJSON(response, http.StatusOK, map[string]any{"tasks": tasks, "hasMore": hasMore})
}

func (server *Server) task(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	task, err := server.store.GetTask(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取任务失败")
		return
	}
	writeJSON(response, http.StatusOK, task)
}

func (server *Server) taskEvents(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	if _, err := server.store.GetTask(request.Context(), request.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(response, http.StatusInternalServerError, "读取任务失败")
		return
	}
	limit := queryLimit(request, 200, 500)
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	events, err := server.store.ListTaskEvents(request.Context(), request.PathValue("id"), after, limit+1)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取任务事件失败")
		return
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	writeJSON(response, http.StatusOK, map[string]any{"events": events, "hasMore": hasMore})
}

func (server *Server) audit(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	limit := queryLimit(request, 50, 200)
	entries, err := server.store.ListAudit(request.Context(), limit+1, queryOffset(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取审计记录失败")
		return
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	writeJSON(response, http.StatusOK, map[string]any{"entries": entries, "hasMore": hasMore})
}

func requireActor(response http.ResponseWriter, request *http.Request) (string, bool) {
	actor := request.Header.Get(actorHeader)
	if !actorPattern.MatchString(actor) {
		writeError(response, http.StatusUnauthorized, "内部身份无效")
		return "", false
	}
	return actor, true
}

func decodeBody(request *http.Request, target any) error {
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		return errors.New("请求必须使用 application/json")
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求 JSON 无效: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func queryLimit(request *http.Request, fallback, maximum int) int {
	value, err := strconv.Atoi(request.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func queryOffset(request *http.Request) int {
	value, err := strconv.Atoi(request.URL.Query().Get("offset"))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]any{"error": redactText(message)})
}

func (server *Server) events(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireActor(response, request); !ok {
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeError(response, http.StatusInternalServerError, "当前连接不支持 SSE")
		return
	}
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	if request.URL.Query().Get("tail") == "1" {
		latest, err := server.store.LatestEventSequence(request.Context())
		if err != nil {
			writeError(response, http.StatusInternalServerError, "读取事件游标失败")
			return
		}
		after = latest
	}
	notifications, unsubscribe := server.engine.Broker().Subscribe()
	defer unsubscribe()
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		events, err := server.store.ListEvents(request.Context(), after, 500)
		if err != nil {
			return
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(response, "id: %d\nevent: ops\ndata: %s\n\n", event.Sequence, data)
			after = event.Sequence
		}
		flusher.Flush()
		select {
		case <-request.Context().Done():
			return
		case <-notifications:
		case <-keepalive.C:
			_, _ = io.WriteString(response, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
