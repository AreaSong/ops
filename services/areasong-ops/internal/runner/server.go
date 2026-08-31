package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const actorHeader = "X-AreaSong-Ops-Actor-Hash"

const (
	runnerIDHeader        = "X-AreaSong-Runner-ID"
	runnerNonceHeader     = "X-AreaSong-Runner-Nonce"
	runnerTimestampHeader = "X-AreaSong-Runner-Timestamp"
	runnerSignatureHeader = "X-AreaSong-Runner-Signature"
)

type listenerKind string

const (
	listenerUnix listenerKind = "unix"
	listenerMTLS listenerKind = "mtls"
)

type listenerKindContextKey struct{}

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
	mux.HandleFunc("GET /v1/states", server.states)
	mux.HandleFunc("GET /v1/services/{name}/state", server.serviceState)
	mux.HandleFunc("POST /v1/services/{name}/reconcile", server.reconcileService)
	mux.HandleFunc("GET /v1/objects", server.objects)
	mux.HandleFunc("GET /v1/automatic-tasks", server.automaticTasks)
	mux.HandleFunc("GET /v1/auto-updates", server.autoUpdatePolicies)
	mux.HandleFunc("PUT /v1/auto-updates/{service}", server.updateAutoUpdatePolicy)
	mux.HandleFunc("POST /v1/auto-updates/evaluate", server.evaluateAutoUpdates)
	mux.HandleFunc("GET /v1/alerts", server.alerts)
	mux.HandleFunc("GET /v1/recovery-center/{service}", server.recoveryCenter)
	mux.HandleFunc("POST /v1/recovery-center/{service}/plan", server.createRestorePlan)
	mux.HandleFunc("GET /v1/compose/{service}", server.composeFile)
	mux.HandleFunc("POST /v1/compose/{service}/revisions", server.proposeCompose)
	mux.HandleFunc("POST /v1/compose/{service}/revisions/{id}/approve", server.approveCompose)
	mux.HandleFunc("POST /v1/compose/{service}/revisions/{id}/apply", server.applyCompose)
	mux.HandleFunc("POST /v1/compose/{service}/revisions/{id}/rollback", server.rollbackCompose)
	mux.HandleFunc("POST /v1/kubernetes/operations", server.kubernetes)
	mux.HandleFunc("POST /v1/kubernetes/plans", server.createKubernetesPlan)
	mux.HandleFunc("GET /v1/kubernetes/plans", server.kubernetesPlans)
	mux.HandleFunc("GET /v1/kubernetes/plans/{id}", server.kubernetesPlan)
	mux.HandleFunc("POST /v1/kubernetes/plans/{id}/approve", server.approveKubernetesPlan)
	mux.HandleFunc("POST /v1/kubernetes/plans/{id}/execute", server.executeKubernetesPlan)
	mux.HandleFunc("GET /v1/terminal/commands", server.terminalCommands)
	mux.HandleFunc("POST /v1/terminal/sessions", server.startTerminal)
	mux.HandleFunc("GET /v1/terminal/shell-plans", server.terminalShellPlans)
	mux.HandleFunc("POST /v1/terminal/shell-plans", server.createTerminalShellPlan)
	mux.HandleFunc("POST /v1/terminal/shell-plans/{id}/approve", server.approveTerminalShellPlan)
	mux.HandleFunc("POST /v1/terminal/shell-plans/{id}/execute", server.executeTerminalShellPlan)
	mux.HandleFunc("GET /v1/files", server.managedFile)
	mux.HandleFunc("GET /v1/files/proposals", server.managedFileProposals)
	mux.HandleFunc("POST /v1/files/proposals", server.proposeManagedFile)
	mux.HandleFunc("POST /v1/files/proposals/{id}/approve", server.approveManagedFileProposal)
	mux.HandleFunc("POST /v1/files/proposals/{id}/apply", server.applyManagedFileProposal)
	mux.HandleFunc("POST /v1/files/proposals/{id}/rollback", server.rollbackManagedFileProposal)
	mux.HandleFunc("POST /v1/extensions", server.uploadExtension)
	mux.HandleFunc("GET /v1/runner/update", server.runnerUpdateStatus)
	mux.HandleFunc("POST /v1/runner/update/prepare", server.prepareRunnerUpdate)
	mux.HandleFunc("POST /v1/runner/update/{id}/activate", server.activateRunnerUpdate)
	mux.HandleFunc("POST /v1/runner/update/{id}/cancel", server.cancelRunnerUpdate)
	mux.HandleFunc("POST /v1/runner/update/{id}/resolve", server.resolveRunnerUpdate)
	mux.HandleFunc("GET /v1/access", server.accessControl)
	mux.HandleFunc("PUT /v1/access", server.updateAccess)
	mux.HandleFunc("GET /v1/access/changes", server.accessChanges)
	mux.HandleFunc("POST /v1/access/changes", server.createAccessChange)
	mux.HandleFunc("POST /v1/access/changes/{id}/approve", server.approveAccessChange)
	mux.HandleFunc("POST /v1/access/changes/{id}/apply", server.applyAccessChange)
	mux.HandleFunc("POST /v1/access/changes/{id}/reject", server.rejectAccessChange)
	mux.HandleFunc("GET /v1/kubernetes", server.kubernetesView)
	mux.HandleFunc("GET /v1/extensions", server.extensionPolicyView)
	mux.HandleFunc("PUT /v1/extensions", server.updateExtensionPolicy)
	mux.HandleFunc("GET /v1/fleet", server.fleet)
	mux.HandleFunc("PUT /v1/fleet/servers/{id}", server.registerServer)
	mux.HandleFunc("PUT /v1/fleet/runners/{id}", server.registerRunner)
	mux.HandleFunc("POST /v1/fleet/runners/{id}/heartbeat", server.runnerHeartbeat)
	mux.HandleFunc("POST /v1/fleet/runners/{id}/assignments/claim", server.claimAssignment)
	mux.HandleFunc("POST /v1/fleet/runners/{id}/assignments/{taskId}/heartbeat", server.assignmentHeartbeat)
	mux.HandleFunc("POST /v1/fleet/runners/{id}/assignments/{taskId}/progress", server.assignmentProgress)
	mux.HandleFunc("POST /v1/fleet/runners/{id}/assignments/{taskId}/events", server.assignmentEvent)
	mux.HandleFunc("POST /v1/fleet/runners/{id}/assignments/{taskId}/complete", server.completeAssignment)
	mux.HandleFunc("GET /v1/credentials/github-alertmanager", server.credentialProfile)
	mux.HandleFunc("POST /v1/credentials/github-alertmanager/rotate", server.rotateCredential)
	mux.HandleFunc("POST /v1/credential-rotations/{id}/close", server.closeCredentialRotation)
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
	mux.HandleFunc("GET /v1/batches", server.batches)
	mux.HandleFunc("GET /v1/batches/{id}", server.batch)
	mux.HandleFunc("POST /v1/batches", server.createBatch)
	mux.HandleFunc("POST /v1/batches/{id}/run", server.runBatch)
	mux.HandleFunc("POST /v1/batches/{id}/approve", server.approveBatch)
	mux.HandleFunc("GET /v1/audit", server.audit)
	mux.HandleFunc("GET /v1/events", server.events)
	return requestLimits(mux)
}

// WithListenerKind marks the transport boundary used for a request. The Unix
// listener remains the local Web/control API; the mTLS listener is restricted
// to the remote Runner heartbeat endpoint.
func WithListenerKind(handler http.Handler, kind string) http.Handler {
	value := listenerKind(strings.ToLower(strings.TrimSpace(kind)))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), listenerKindContextKey{}, value)
		handler.ServeHTTP(response, request.WithContext(ctx))
	})
}

// RemoteRunnerHandler exposes only the authenticated Runner protocol on the
// TCP/mTLS listener. The full control-plane API remains reachable through the
// Unix listener used by the local Web process.
func RemoteRunnerHandler(next http.Handler) http.Handler {
	return WithListenerKind(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		valid := request.Method == http.MethodPost && len(parts) >= 5 &&
			parts[0] == "v1" && parts[1] == "fleet" && parts[2] == "runners"
		if valid {
			valid = (len(parts) == 5 && parts[4] == "heartbeat") ||
				(len(parts) == 6 && parts[4] == "assignments" && parts[5] == "claim") ||
				(len(parts) == 7 && parts[4] == "assignments" &&
					(parts[6] == "heartbeat" || parts[6] == "progress" || parts[6] == "events" || parts[6] == "complete"))
		}
		if !valid {
			writeError(response, http.StatusNotFound, "远程 Runner 监听器仅提供受控执行协议")
			return
		}
		next.ServeHTTP(response, request)
	}), string(listenerMTLS))
}

func (server *Server) assignmentIdentity(request *http.Request) (string, bool) {
	id := request.PathValue("id")
	node, found, err := server.store.GetRunnerNode(request.Context(), id)
	if err != nil || !found || requireRunnerTransportIdentity(request, server.engine.catalog.Fleet, node) != nil {
		return "", false
	}
	return id, true
}

func (server *Server) claimAssignment(response http.ResponseWriter, request *http.Request) {
	runnerID, ok := server.assignmentIdentity(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "Runner 身份无效")
		return
	}
	var input model.AssignmentClaimRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	lease := assignmentLeaseDuration(input.LeaseSeconds, server.engine.catalog.Fleet)
	assignment, task, claimed, err := server.store.ClaimTaskAssignment(request.Context(), runnerID, input.TaskID, lease)
	if err != nil {
		writeAssignmentError(response, err)
		return
	}
	if !claimed {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(response, http.StatusOK, model.AssignmentClaimResponse{Task: model.NewTaskDispatch(task), Assignment: assignment})
}

func (server *Server) assignmentHeartbeat(response http.ResponseWriter, request *http.Request) {
	runnerID, ok := server.assignmentIdentity(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "Runner 身份无效")
		return
	}
	var input model.AssignmentHeartbeatRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	assignment, err := server.store.HeartbeatTaskAssignment(request.Context(), runnerID, request.PathValue("taskId"), input.AssignmentFence, assignmentLeaseDuration(0, server.engine.catalog.Fleet))
	if err != nil {
		writeAssignmentError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, assignment)
}

func (server *Server) assignmentProgress(response http.ResponseWriter, request *http.Request) {
	runnerID, ok := server.assignmentIdentity(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "Runner 身份无效")
		return
	}
	var input model.AssignmentProgressRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	assignment, err := server.store.UpdateTaskAssignmentProgress(request.Context(), runnerID, request.PathValue("taskId"), input, assignmentLeaseDuration(0, server.engine.catalog.Fleet))
	if err != nil {
		writeAssignmentError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, assignment)
}

func (server *Server) assignmentEvent(response http.ResponseWriter, request *http.Request) {
	runnerID, ok := server.assignmentIdentity(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "Runner 身份无效")
		return
	}
	var input model.AssignmentEventRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	event, err := server.store.AppendTaskAssignmentEvent(request.Context(), runnerID, request.PathValue("taskId"), input, assignmentLeaseDuration(0, server.engine.catalog.Fleet))
	if err != nil {
		writeAssignmentError(response, err)
		return
	}
	server.engine.broker.Publish(event.Sequence)
	writeJSON(response, http.StatusOK, event)
}

func (server *Server) completeAssignment(response http.ResponseWriter, request *http.Request) {
	runnerID, ok := server.assignmentIdentity(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "Runner 身份无效")
		return
	}
	var input model.AssignmentCompletionRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	taskID := request.PathValue("taskId")
	taskBefore, taskErr := server.store.GetTask(request.Context(), taskID)
	if taskErr != nil {
		writeAssignmentError(response, taskErr)
		return
	}
	completedForDesired := taskBefore
	completedForDesired.State = input.State
	desired := server.engine.desiredStateInputForTask(completedForDesired)
	task, assignment, sequence, err := server.store.CompleteTaskAssignmentWithDesired(request.Context(), runnerID, taskID, input, desired)
	if err != nil {
		writeAssignmentError(response, err)
		return
	}
	server.engine.broker.Publish(sequence)
	writeJSON(response, http.StatusOK, model.AssignmentCompletionResponse{Task: task, Assignment: assignment, EventSequence: sequence})
}

func assignmentLeaseDuration(seconds int, policy *config.FleetPolicy) time.Duration {
	if seconds <= 0 && policy != nil {
		seconds = policy.HeartbeatTimeoutSeconds
	}
	if seconds <= 0 {
		seconds = 90
	}
	return time.Duration(seconds) * time.Second
}

func writeAssignmentError(response http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, store.ErrAssignmentNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, store.ErrAssignmentFence) || errors.Is(err, store.ErrAssignmentExpired) ||
		errors.Is(err, store.ErrAssignmentCompleted) {
		status = http.StatusPreconditionFailed
	}
	writeError(response, status, err.Error())
}

func (server *Server) autoUpdatePolicies(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	items, err := server.engine.AutoUpdatePolicies(request.Context(), actor)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"policies": items})
}

func (server *Server) terminalShellPlans(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	items, err := server.engine.TerminalShellPlans(request.Context(), actor)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"plans": items})
}

func (server *Server) managedFileProposals(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	items, err := server.engine.ManagedFileProposals(request.Context(), actor)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"proposals": items})
}

func (server *Server) approveManagedFileProposal(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ManagedFileApprovalRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	proposal, err := server.engine.ApproveManagedFileProposal(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, proposal)
}

func (server *Server) applyManagedFileProposal(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ManagedFileApplyRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	proposal, err := server.engine.ApplyManagedFileProposal(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{"error": redactText(err.Error()), "proposal": proposal})
		return
	}
	writeJSON(response, http.StatusOK, proposal)
}

func (server *Server) rollbackManagedFileProposal(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ManagedFileRollbackRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	proposal, err := server.engine.RollbackManagedFileProposal(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{"error": redactText(err.Error()), "proposal": proposal})
		return
	}
	writeJSON(response, http.StatusOK, proposal)
}

func (server *Server) createTerminalShellPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.TerminalShellPlanRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, created, err := server.engine.CreateTerminalShellPlan(request.Context(), actor, input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, plan)
}

func (server *Server) approveTerminalShellPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.TerminalShellApprovalRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := server.engine.ApproveTerminalShellPlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (server *Server) executeTerminalShellPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.TerminalShellExecuteRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := server.engine.ExecuteTerminalShellPlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{"error": redactText(err.Error()), "plan": plan})
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (server *Server) updateAutoUpdatePolicy(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.AutoUpdatePolicyRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Service = request.PathValue("service")
	policy, err := server.engine.UpdateAutoUpdatePolicy(request.Context(), actor, input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, policy)
}

func (server *Server) evaluateAutoUpdates(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	items, err := server.engine.EvaluateAutoUpdates(request.Context(), actor)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"evaluations": items})
}

func (server *Server) credentialProfile(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.authorizePlatform(request.Context(), actor, model.PermissionRead, "credentials"); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	profile, err := server.engine.CredentialProfile(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, profile)
}

func (server *Server) rotateCredential(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.CredentialRotationRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	rotation, created, err := server.engine.RotateCredential(request.Context(), actor, input)
	input.Secret = ""
	if err != nil {
		status := http.StatusConflict
		if rotation.ID == "" {
			writeError(response, status, err.Error())
			return
		}
		writeJSON(response, status, map[string]any{
			"error": redactText(err.Error()), "rotation": rotation,
		})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, rotation)
}

func (server *Server) closeCredentialRotation(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.CredentialRotationCloseRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	rotation, _, err := server.engine.CloseCredentialRotation(
		request.Context(), actor, request.PathValue("id"), input)
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{
			"error": redactText(err.Error()), "rotation": rotation,
		})
		return
	}
	writeJSON(response, http.StatusOK, rotation)
}

func (server *Server) cancelRunnerUpdate(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.RunnerUpdateCancellationRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	update, _, err := server.engine.CancelRunnerUpdate(
		request.Context(), actor, request.PathValue("id"), input,
	)
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{
			"error": redactText(err.Error()), "update": update,
		})
		return
	}
	writeJSON(response, http.StatusOK, update)
}

func (server *Server) objects(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	objects, err := server.engine.ObjectsForActor(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"objects": objects})
}

func (server *Server) automaticTasks(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	tasks, err := server.engine.AutomaticTasksForActor(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"automaticTasks": tasks})
}

func (server *Server) alerts(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.validateActorTenant(request.Context(), actor); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	alerts, err := server.engine.ActiveAlerts(request.Context())
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "Alertmanager 活动告警当前不可用")
		return
	}
	visible := make([]model.ActiveAlert, 0, len(alerts))
	for _, alert := range alerts {
		if server.engine.visibleAlert(request.Context(), actor, alert) {
			visible = append(visible, alert)
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"alerts": visible})
}

func (server *Server) recoveryCenter(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	service, exists := server.engine.catalog.Object(request.PathValue("service"))
	if !exists || service.Metadata.Type != "service" {
		writeError(response, http.StatusNotFound, "服务未纳入控制面")
		return
	}
	if err := server.engine.authorize(request.Context(), actor, model.PermissionRead, service.ObjectID); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	view, err := server.engine.RecoveryCenter(request.Context(), request.PathValue("service"))
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *Server) createRestorePlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.RestoreRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Service = request.PathValue("service")
	if service, exists := server.engine.catalog.Object(input.Service); !exists ||
		server.engine.authorize(request.Context(), actor, model.PermissionRecover, service.ObjectID) != nil {
		writeError(response, http.StatusForbidden, "当前角色没有恢复权限")
		return
	}
	plan, err := server.engine.CreateRestorePlan(request.Context(), actor, input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, plan)
}

func (server *Server) composeFile(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	service, exists := server.engine.catalog.Object(request.PathValue("service"))
	if !exists || server.engine.authorize(request.Context(), actor, model.PermissionRead, service.ObjectID) != nil {
		writeError(response, http.StatusForbidden, "当前角色没有读取该服务的权限")
		return
	}
	view, err := server.engine.ComposeFile(request.Context(), request.PathValue("service"))
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *Server) proposeCompose(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ComposeEditRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Service = request.PathValue("service")
	if service, exists := server.engine.catalog.Object(input.Service); !exists ||
		server.engine.authorize(request.Context(), actor, model.PermissionManageConfig, service.ObjectID) != nil {
		writeError(response, http.StatusForbidden, "当前角色没有修改该服务配置的权限")
		return
	}
	revision, err := server.engine.ProposeCompose(request.Context(), actor, input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, revision)
}

func (server *Server) approveCompose(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ComposeApprovalRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	revision, err := server.engine.ApproveComposeRevision(
		request.Context(), actor, request.PathValue("service"), request.PathValue("id"), input,
	)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, revision)
}

func (server *Server) applyCompose(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ComposeApplyRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	revision, err := server.engine.ApplyComposeRevision(
		request.Context(), actor, request.PathValue("service"), request.PathValue("id"), input,
	)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{
			"error": redactText(err.Error()), "revision": revision,
		})
		return
	}
	writeJSON(response, http.StatusOK, revision)
}

func (server *Server) rollbackCompose(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ComposeRollbackRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	revision, err := server.engine.RollbackComposeRevision(
		request.Context(), actor, request.PathValue("service"), request.PathValue("id"), input,
	)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{
			"error": redactText(err.Error()), "revision": revision,
		})
		return
	}
	writeJSON(response, http.StatusOK, revision)
}

func (server *Server) kubernetes(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.KubernetesRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	op, output, err := server.engine.Kubernetes(request.Context(), actor, input)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{"error": redactText(err.Error()), "output": output})
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"operation": op, "output": output})
}

func (server *Server) createKubernetesPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.KubernetesPlanRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, created, err := server.engine.CreateKubernetesPlan(request.Context(), actor, input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, plan)
}

func (server *Server) kubernetesPlans(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	plans, err := server.engine.KubernetesPlans(request.Context(), actor, queryLimit(request, 50, 200))
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusInternalServerError)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"plans": plans})
}

func (server *Server) kubernetesPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	plan, err := server.engine.KubernetesPlan(request.Context(), actor, request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusInternalServerError)
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (server *Server) approveKubernetesPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.KubernetesPlanApprovalRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := server.engine.ApproveKubernetesPlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (server *Server) executeKubernetesPlan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.KubernetesPlanExecuteRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	op, err := server.engine.ExecuteKubernetesPlan(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"operation": op})
}

func (server *Server) terminalCommands(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	commands, err := server.engine.TerminalCommands(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"commands": commands})
}

func (server *Server) startTerminal(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.TerminalStartRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	output, err := server.engine.RunTerminal(request.Context(), actor, input)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{"error": redactText(err.Error()), "output": output})
		return
	}
	writeJSON(response, http.StatusOK, output)
}

func (server *Server) managedFile(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	view, err := server.engine.ManagedFile(request.Context(), actor,
		request.URL.Query().Get("root"), request.URL.Query().Get("path"))
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(response, status, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *Server) proposeManagedFile(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ManagedFileRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	proposal, created, err := server.engine.ProposeManagedFile(request.Context(), actor, input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, proposal)
}

func (server *Server) uploadExtension(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ExtensionUploadRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	result, created, err := server.engine.UploadExtension(request.Context(), actor, input)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{"error": redactText(err.Error()), "result": result})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, result)
}

func (server *Server) runnerUpdateStatus(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	status, err := server.engine.RunnerUpdateStatus(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, status)
}

func (server *Server) prepareRunnerUpdate(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.RunnerUpdateRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	update, created, err := server.engine.PrepareRunnerUpdate(request.Context(), actor, input)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(response, status, update)
}

func (server *Server) activateRunnerUpdate(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.RunnerUpdateActivationRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	update, created, err := server.engine.ActivateRunnerUpdate(
		request.Context(), actor, request.PathValue("id"), input,
	)
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]any{
			"error": redactText(err.Error()), "update": update,
		})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(response, status, update)
}

func (server *Server) resolveRunnerUpdate(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.RunnerUpdateResolutionRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	update, _, err := server.engine.ResolveRunnerUpdate(
		request.Context(), actor, request.PathValue("id"), input,
	)
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, update)
}

func (server *Server) accessControl(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	view, err := server.engine.AccessControl(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *Server) updateAccess(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.AccessControlUpdateRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	// Schema 4 treats every access-policy mutation as high risk. The client
	// must not be able to downgrade the public PUT endpoint by sending
	// requiresDualApproval=false; the only direct-write compatibility path is
	// the legacy schema-3 control plane. Approved changes are applied through
	// ApplyAccessChange, which is an internal path and is not routed here.
	if server.engine.catalog.SchemaVersion >= 4 {
		input.RequiresDualApproval = true
	}
	if input.RequiresDualApproval {
		change, created, err := server.engine.CreateAccessChange(request.Context(), actor, input)
		if err != nil {
			writeAuthorizationOrError(response, err, http.StatusConflict)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusAccepted
		}
		writeJSON(response, status, change)
		return
	}
	view, err := server.engine.UpdateAccess(request.Context(), actor, input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *Server) accessChanges(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	changes, err := server.engine.AccessChanges(request.Context(), actor)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"changes": changes})
}

func (server *Server) createAccessChange(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.AccessControlUpdateRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.RequiresDualApproval = true
	change, created, err := server.engine.CreateAccessChange(request.Context(), actor, input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(response, status, change)
}

func (server *Server) approveAccessChange(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.AccessChangeApprovalRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	change, err := server.engine.ApproveAccessChange(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, change)
}

func (server *Server) applyAccessChange(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	change, err := server.engine.ApplyAccessChange(request.Context(), actor, request.PathValue("id"))
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, change)
}

func (server *Server) rejectAccessChange(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	change, err := server.engine.RejectAccessChange(request.Context(), actor, request.PathValue("id"), input.Reason)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, change)
}

func (server *Server) kubernetesView(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	view, err := server.engine.KubernetesView(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *Server) extensionPolicyView(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	view, err := server.engine.ExtensionPolicyView(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *Server) updateExtensionPolicy(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.ExtensionPolicyUpdateRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if !uuidPattern.MatchString(input.IdempotencyKey) {
		writeError(response, http.StatusBadRequest, "扩展策略幂等键无效")
		return
	}
	if err := server.engine.authorizePlatform(request.Context(), actor, model.PermissionManageConfig, "extensions"); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeError(response, http.StatusConflict, "扩展策略必须通过受控配置变更，不支持网页直接覆盖")
}

func (server *Server) fleet(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.validateActorTenant(request.Context(), actor); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	fleet, err := server.engine.FleetForActor(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, fleet)
}

func (server *Server) registerServer(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var node model.ServerNode
	if err := decodeBody(request, &node); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if node.ID != request.PathValue("id") {
		writeError(response, http.StatusBadRequest, "节点标识与路径不一致")
		return
	}
	if err := server.engine.RegisterServer(request.Context(), actor, node); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, node)
}

func (server *Server) registerRunner(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var node model.RunnerNode
	if err := decodeBody(request, &node); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if node.ID != request.PathValue("id") {
		writeError(response, http.StatusBadRequest, "Runner 标识与路径不一致")
		return
	}
	if err := server.engine.RegisterRunner(request.Context(), actor, node); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, node)
}

func (server *Server) runnerHeartbeat(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	policy := server.engine.catalog.Fleet
	node, found, err := server.store.GetRunnerNode(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "读取 Runner 身份失败")
		return
	}
	if !found {
		writeError(response, http.StatusUnauthorized, "Runner 未登记")
		return
	}
	if err := requireRunnerIdentity(request, policy, node); err != nil {
		writeError(response, http.StatusUnauthorized, err.Error())
		return
	}
	var input RunnerHeartbeatRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if input.RunnerID == "" {
		input.RunnerID = id
	}
	if input.PayloadVersion == 0 {
		input.PayloadVersion = HeartbeatPayloadVersion
	}
	if nonce := request.Header.Get(runnerNonceHeader); input.Nonce == "" {
		input.Nonce = nonce
	} else if nonce != "" && input.Nonce != nonce {
		writeError(response, http.StatusBadRequest, "Runner 心跳 nonce 不一致")
		return
	}
	if timestamp := request.Header.Get(runnerTimestampHeader); input.Timestamp == "" {
		input.Timestamp = timestamp
	} else if timestamp != "" && input.Timestamp != timestamp {
		writeError(response, http.StatusBadRequest, "Runner 心跳时间戳不一致")
		return
	}
	if signature := request.Header.Get(runnerSignatureHeader); input.Signature == "" {
		input.Signature = signature
	} else if signature != "" && input.Signature != signature {
		writeError(response, http.StatusBadRequest, "Runner 心跳签名不一致")
		return
	}
	if err := verifyRunnerHeartbeat(policy, node, input); err != nil {
		writeError(response, http.StatusUnauthorized, err.Error())
		return
	}
	payloadDigest, err := HeartbeatPayloadDigest(input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	node, err = server.engine.HeartbeatRunnerAuthenticated(request.Context(), id, input, payloadDigest)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, node)
}

func requireRunnerIdentity(request *http.Request, policy *config.FleetPolicy, node model.RunnerNode) error {
	if err := requireRunnerTransportIdentity(request, policy, node); err != nil {
		return err
	}
	if nonce := request.Header.Get(runnerNonceHeader); nonce == "" || len(nonce) > 128 || strings.ContainsAny(nonce, " \t\r\n") {
		return errors.New("Runner 心跳 nonce 无效")
	}
	if request.Header.Get(runnerTimestampHeader) == "" {
		return errors.New("Runner 心跳缺少时间戳")
	}
	return nil
}

func requireRunnerTransportIdentity(request *http.Request, policy *config.FleetPolicy, node model.RunnerNode) error {
	if policy == nil || !policy.Enabled {
		return errors.New("多服务器管理尚未启用")
	}
	if request.Header.Get(runnerIDHeader) != node.ID {
		return errors.New("Runner 身份头缺失或不匹配")
	}
	kind, _ := request.Context().Value(listenerKindContextKey{}).(listenerKind)
	// An explicitly marked Unix listener is trusted as the local Web boundary.
	// Direct/in-process calls without a listener marker stay fail-closed when
	// mTLS is required, which also prevents test and embedded HTTP callers from
	// accidentally bypassing the transport contract.
	remote := kind == listenerMTLS || (kind == "" && request.TLS == nil && policy.RequiremTLS) || request.TLS != nil
	if remote && !policy.AllowRemoteRunners {
		return errors.New("当前策略不允许远程 Runner")
	}
	if kind == "" && request.TLS == nil && policy.RequiremTLS {
		return errors.New("Runner 心跳必须来自 Unix 或已验证 mTLS 监听器")
	}
	if remote {
		if !policy.RequiremTLS {
			return errors.New("远程 Runner 必须启用 mTLS 策略")
		}
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 || len(request.TLS.VerifiedChains) == 0 {
			return errors.New("Runner 心跳必须使用已验证 mTLS")
		}
		certificate := request.TLS.PeerCertificates[0]
		matched := certificate.Subject.CommonName == node.ID
		for _, name := range certificate.DNSNames {
			matched = matched || name == node.ID
		}
		for _, uri := range certificate.URIs {
			matched = matched || uri.String() == node.ID || strings.HasSuffix(uri.String(), "/"+node.ID)
		}
		if !matched {
			return errors.New("mTLS 证书未声明该 Runner ID")
		}
		if node.CertificateFingerprint == "" {
			return errors.New("远程 Runner 缺少已固定的证书指纹")
		}
		fingerprint := certificateFingerprint(certificate.Raw)
		if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(normalizeCertificateFingerprint(node.CertificateFingerprint))) != 1 {
			return errors.New("Runner mTLS 证书指纹不匹配")
		}
	}
	return nil
}

func verifyRunnerHeartbeat(policy *config.FleetPolicy, node model.RunnerNode, input RunnerHeartbeatRequest) error {
	timestamp, err := time.Parse(time.RFC3339Nano, input.Timestamp)
	if err != nil {
		return errors.New("Runner 心跳时间戳格式无效")
	}
	maxSkew := time.Duration(policy.HeartbeatMaxSkewSeconds) * time.Second
	if maxSkew <= 0 {
		maxSkew = 5 * time.Minute
	}
	now := time.Now().UTC()
	if timestamp.Before(now.Add(-maxSkew)) || timestamp.After(now.Add(maxSkew)) {
		return errors.New("Runner 心跳时间戳已过期或超前")
	}
	encodedKey := strings.TrimSpace(node.HeartbeatPublicKey)
	if encodedKey == "" && policy.RunnerPublicKeys != nil {
		encodedKey = strings.TrimSpace(policy.RunnerPublicKeys[node.ID])
	}
	if encodedKey == "" {
		if policy.RequireSignedHeartbeat || policy.AllowRemoteRunners {
			return errors.New("Runner 心跳缺少已登记 Ed25519 公钥")
		}
		return nil
	}
	publicKey, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("Runner 心跳公钥无效")
	}
	verified, err := VerifyHeartbeatPayload(ed25519.PublicKey(publicKey), input)
	if err != nil {
		return err
	}
	if !verified {
		return errors.New("Runner 心跳签名验证失败")
	}
	return nil
}

func certificateFingerprint(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalizeCertificateFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, "sha256:") {
		value = "sha256:" + value
	}
	return value
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

func (server *Server) batches(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.validateActorTenant(request.Context(), actor); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	items, hasMore, err := server.engine.BatchOperationsForActor(request.Context(), actor,
		queryLimit(request, 50, 200), queryOffset(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取批量作业失败")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"batches": items, "hasMore": hasMore})
}

func (server *Server) batch(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	item, err := server.engine.BatchOperation(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取批量作业失败")
		return
	}
	if err := server.engine.authorizeBatchRead(request.Context(), actor, item); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, item)
}

func (server *Server) createBatch(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.BatchCreateRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	item, created, err := server.engine.CreateBatch(request.Context(), actor, input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, item)
}

func (server *Server) runBatch(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.BatchExecuteRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	item, err := server.engine.ExecuteBatch(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusAccepted, item)
}

func (server *Server) approveBatch(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	var input model.BatchApproveRequest
	if err := decodeBody(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	item, err := server.engine.ApproveBatch(request.Context(), actor, request.PathValue("id"), input)
	if err != nil {
		writeAuthorizationOrError(response, err, http.StatusConflict)
		return
	}
	writeJSON(response, http.StatusOK, item)
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
	if !uuidPattern.MatchString(input.IdempotencyKey) {
		writeError(response, http.StatusBadRequest, "发布计划必须携带有效幂等键")
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
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.validateActorTenant(request.Context(), actor); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	limit := queryLimit(request, 50, 200)
	plans, hasMore, err := server.engine.ReleasePlansForActor(request.Context(), actor, limit, queryOffset(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取发布计划失败")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"plans": plans, "hasMore": hasMore})
}

func (server *Server) plan(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
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
	if err := server.engine.authorizePlan(request.Context(), actor, plan, model.PermissionRead); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
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
			request.Body = http.MaxBytesReader(response, request.Body, 96<<20)
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
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	services, err := server.engine.ServicesForActor(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"services": services})
}

func (server *Server) states(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	states, err := server.engine.ServiceStatesForActor(request.Context(), actor)
	if err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"states": states})
}

func (server *Server) serviceState(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	service, exists := server.engine.catalog.Object(request.PathValue("name"))
	if !exists || server.engine.authorize(request.Context(), actor, model.PermissionRead, service.ObjectID) != nil {
		writeError(response, http.StatusForbidden, "当前角色没有读取该服务状态的权限")
		return
	}
	state, err := server.engine.ServiceState(request.Context(), request.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, state)
}

func (server *Server) reconcileService(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	service, exists := server.engine.catalog.Object(request.PathValue("name"))
	if !exists || server.engine.authorize(request.Context(), actor, model.PermissionRead, service.ObjectID) != nil {
		writeError(response, http.StatusForbidden, "当前角色没有读取该服务状态的权限")
		return
	}
	state, err := server.engine.ServiceState(request.Context(), request.PathValue("name"))
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"state": state, "actionRequired": state.Drift != nil && state.Drift.Detected})
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
	if service, exists := server.engine.catalog.Object(input.Service); exists {
		if err := server.engine.authorize(request.Context(), actor, permissionForAction(input.Action), service.ObjectID); err != nil {
			writeError(response, http.StatusForbidden, err.Error())
			return
		}
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
	if preview, err := server.store.GetPreview(request.Context(), input.PreviewID); err == nil {
		if service, exists := server.engine.catalog.Object(preview.Service); exists {
			if err := server.engine.authorize(request.Context(), actor, permissionForAction(preview.Action), service.ObjectID); err != nil {
				writeError(response, http.StatusForbidden, err.Error())
				return
			}
		}
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
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.validateActorTenant(request.Context(), actor); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	limit := queryLimit(request, 50, 200)
	tasks, hasMore, err := server.engine.TasksForActor(request.Context(), actor, limit, queryOffset(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取任务失败")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"tasks": tasks, "hasMore": hasMore})
}

func (server *Server) task(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
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
	if err := server.engine.authorizeTask(request.Context(), actor, task, model.PermissionRead); err != nil {
		writeError(response, http.StatusForbidden, "当前角色没有读取该任务的权限")
		return
	}
	writeJSON(response, http.StatusOK, task)
}

func (server *Server) taskEvents(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	task, err := server.store.GetTask(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(response, http.StatusInternalServerError, "读取任务失败")
		return
	}
	if err := server.engine.authorizeTask(request.Context(), actor, task, model.PermissionRead); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
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
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.validateActorTenant(request.Context(), actor); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	limit := queryLimit(request, 50, 200)
	entries, hasMore, err := server.engine.AuditForActor(request.Context(), actor, limit, queryOffset(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "读取审计记录失败")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"entries": entries, "hasMore": hasMore})
}

// validateActorTenant validates the caller before a filtered collection is
// read. Collection endpoints cannot authorize a synthetic object such as
// "tasks" because that would reject a legitimate object-scoped binding.
func (engine *Engine) validateActorTenant(ctx context.Context, actor string) error {
	if !actorPattern.MatchString(actor) {
		return authorizationError{message: "操作者标识无效"}
	}
	policy, _, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return authorizationError{message: "访问策略快照不可用"}
	}
	if policy == nil || !policy.Enforced {
		if engine.catalog.SchemaVersion <= 3 {
			return nil
		}
		return authorizationError{message: "生产访问策略未启用"}
	}
	principal, ok := policy.Principals[actor]
	if !ok {
		return authorizationError{message: "操作者未登记"}
	}
	if !principalUsable(policy, actor, principal, time.Now().UTC()) {
		return authorizationError{message: "操作者已禁用、过期或不满足 JIT 授权"}
	}
	tenantID := principal.TenantID
	if tenantID == "" {
		tenantID = policy.DefaultTenant
	}
	if !tenantIsActive(policy, tenantID) {
		return authorizationError{message: "操作者所属租户未启用"}
	}
	return nil
}

func (engine *Engine) TasksForActor(
	ctx context.Context,
	actor string,
	limit, offset int,
) ([]model.Task, bool, error) {
	if err := engine.validateActorTenant(ctx, actor); err != nil {
		return nil, false, err
	}
	all := make([]model.Task, 0)
	for pageOffset := 0; ; pageOffset += collectionScanPageSize {
		page, err := engine.store.ListTasks(ctx, collectionScanPageSize, pageOffset)
		if err != nil {
			return nil, false, err
		}
		for _, task := range page {
			err := engine.authorizeTask(ctx, actor, task, model.PermissionRead)
			if err == nil {
				all = append(all, task)
			} else if !isAuthorizationError(err) {
				return nil, false, err
			}
		}
		if len(page) < collectionScanPageSize {
			break
		}
	}
	return paginateCollection(all, limit, offset)
}

func (engine *Engine) ReleasePlansForActor(
	ctx context.Context,
	actor string,
	limit, offset int,
) ([]model.ReleasePlan, bool, error) {
	if err := engine.validateActorTenant(ctx, actor); err != nil {
		return nil, false, err
	}
	all := make([]model.ReleasePlan, 0)
	for pageOffset := 0; ; pageOffset += collectionScanPageSize {
		page, err := engine.store.ListReleasePlans(ctx, collectionScanPageSize, pageOffset)
		if err != nil {
			return nil, false, err
		}
		for _, plan := range page {
			err := engine.authorizePlan(ctx, actor, plan, model.PermissionRead)
			if err == nil {
				all = append(all, plan)
			} else if !isAuthorizationError(err) {
				return nil, false, err
			}
		}
		if len(page) < collectionScanPageSize {
			break
		}
	}
	return paginateCollection(all, limit, offset)
}

func (engine *Engine) AuditForActor(
	ctx context.Context,
	actor string,
	limit, offset int,
) ([]model.AuditEntry, bool, error) {
	if err := engine.validateActorTenant(ctx, actor); err != nil {
		return nil, false, err
	}
	all := make([]model.AuditEntry, 0)
	for pageOffset := 0; ; pageOffset += collectionScanPageSize {
		page, err := engine.store.ListAudit(ctx, collectionScanPageSize, pageOffset)
		if err != nil {
			return nil, false, err
		}
		for _, entry := range page {
			visible, err := engine.auditEntryVisible(ctx, actor, entry)
			if err != nil {
				return nil, false, err
			}
			if visible {
				all = append(all, entry)
			}
		}
		if len(page) < collectionScanPageSize {
			break
		}
	}
	return paginateCollection(all, limit, offset)
}

const collectionScanPageSize = 200

func paginateCollection[T any](items []T, limit, offset int) ([]T, bool, error) {
	if limit <= 0 {
		limit = 1
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return make([]T, 0), false, nil
	}
	end := offset + limit
	if end < offset || end > len(items) {
		end = len(items)
	}
	result := append([]T(nil), items[offset:end]...)
	return result, end < len(items), nil
}

func (engine *Engine) auditEntryVisible(
	ctx context.Context,
	actor string,
	entry model.AuditEntry,
) (bool, error) {
	resource := strings.TrimSpace(entry.Resource)
	if resource == "" {
		return false, nil
	}
	if task, err := engine.store.GetTask(ctx, resource); err == nil {
		return engine.authorizeTask(ctx, actor, task, model.PermissionRead) == nil, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if plan, err := engine.store.GetReleasePlan(ctx, resource); err == nil {
		return engine.authorizePlan(ctx, actor, plan, model.PermissionRead) == nil, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if operation, err := engine.store.GetBatchOperation(ctx, resource); err == nil {
		return engine.authorizeBatchRead(ctx, actor, operation) == nil, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if preview, err := engine.store.GetPreview(ctx, resource); err == nil {
		object, exists := engine.catalog.Object(preview.Service)
		if !exists {
			return false, nil
		}
		return engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID) == nil, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if object, exists := engine.catalog.Object(resource); exists {
		return engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID) == nil, nil
	}
	if object, exists := engine.catalogObjectByID(resource); exists {
		return engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID) == nil, nil
	}
	for _, prefix := range []string{"service:", "automatic-task:"} {
		if strings.HasPrefix(resource, prefix) {
			if object, exists := engine.catalog.Object(strings.TrimPrefix(resource, prefix)); exists {
				return engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID) == nil, nil
			}
			return false, nil
		}
	}
	if strings.Contains(resource, "/") {
		if object, exists := engine.catalog.Object(strings.SplitN(resource, "/", 2)[0]); exists {
			return engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID) == nil, nil
		}
	}
	if strings.HasPrefix(resource, "fleet:") || strings.HasPrefix(resource, "runner:") || strings.HasPrefix(resource, "server:") {
		return engine.authorizePlatform(ctx, actor, model.PermissionRead, resource) == nil, nil
	}
	if engine.fleetNodeExists(resource) {
		return engine.authorizePlatform(ctx, actor, model.PermissionRead, "fleet:"+resource) == nil, nil
	}
	// Resources without an object identity (for example access or credential
	// policy changes) remain visible only to a platform-scoped audit reader.
	return engine.authorizePlatform(ctx, actor, model.PermissionRead, "audit") == nil, nil
}

func (engine *Engine) catalogObjectByID(objectID string) (model.ServiceDefinition, bool) {
	for _, name := range engine.catalog.ObjectNames() {
		object, exists := engine.catalog.Object(name)
		if exists && object.ObjectID == objectID {
			return object, true
		}
	}
	return model.ServiceDefinition{}, false
}

func (engine *Engine) fleetNodeExists(id string) bool {
	if engine.catalog.Fleet == nil {
		return false
	}
	for _, node := range engine.catalog.Fleet.Inventory.Servers {
		if node.ID == id {
			return true
		}
	}
	for _, node := range engine.catalog.Fleet.Inventory.Runners {
		if node.ID == id {
			return true
		}
	}
	return false
}

func (engine *Engine) FleetForActor(ctx context.Context, actor string) (model.Fleet, error) {
	if err := engine.validateActorTenant(ctx, actor); err != nil {
		return model.Fleet{}, err
	}
	fleet, err := engine.Fleet(ctx)
	if err != nil {
		return model.Fleet{}, err
	}
	visibleServers := make(map[string]bool, len(fleet.Servers))
	filtered := model.Fleet{
		Servers:   make([]model.ServerNode, 0, len(fleet.Servers)),
		Runners:   make([]model.RunnerNode, 0, len(fleet.Runners)),
		CanManage: engine.authorizePlatform(ctx, actor, model.PermissionManageFleet, "fleet") == nil,
	}
	for _, server := range fleet.Servers {
		if engine.fleetNodeVisible(ctx, actor, "fleet:"+server.ID, server.ID) {
			visibleServers[server.ID] = true
			filtered.Servers = append(filtered.Servers, server)
		}
	}
	for _, runner := range fleet.Runners {
		visible := engine.fleetNodeVisible(ctx, actor, "fleet:"+runner.ID, runner.ServerID)
		if visible {
			filtered.Runners = append(filtered.Runners, runner)
			if !visibleServers[runner.ServerID] {
				for _, server := range fleet.Servers {
					if server.ID == runner.ServerID {
						filtered.Servers = append(filtered.Servers, server)
						visibleServers[server.ID] = true
						break
					}
				}
			}
		}
	}
	return filtered, nil
}

func (engine *Engine) fleetNodeVisible(
	ctx context.Context,
	actor, resource, serverID string,
) bool {
	if engine.authorizePlatform(ctx, actor, model.PermissionRead, resource) == nil {
		return true
	}
	for _, name := range engine.catalog.ObjectNames() {
		object, exists := engine.catalog.Object(name)
		if !exists || object.ServerID != serverID {
			continue
		}
		if engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID) == nil {
			return true
		}
	}
	return false
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

func writeAuthorizationOrError(response http.ResponseWriter, err error, fallback int) {
	if isAuthorizationError(err) {
		writeError(response, http.StatusForbidden, err.Error())
		return
	}
	writeError(response, fallback, err.Error())
}

func (server *Server) events(response http.ResponseWriter, request *http.Request) {
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	if err := server.engine.validateActorTenant(request.Context(), actor); err != nil {
		writeError(response, http.StatusForbidden, err.Error())
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
			// Advance the cursor for every row, including rows hidden by the
			// object policy, otherwise a tenant would repeatedly re-read the
			// same unauthorized event forever.
			after = event.Sequence
			if !server.engine.visibleEvent(request.Context(), actor, event) {
				continue
			}
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(response, "id: %d\nevent: ops\ndata: %s\n\n", event.Sequence, data)
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
