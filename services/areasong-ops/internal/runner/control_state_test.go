package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestSetDesiredStateWithRequestReplaysWithoutGenerationOrEvent(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	service := engine.catalog.Services["demo"]
	service.Metadata.Type = "service"
	service.Metadata.Lifecycle = "active"
	engine.catalog.Services["demo"] = service
	ctx := context.Background()
	key := "desired-state-request-1"

	first, err := engine.SetDesiredStateWithRequest(ctx, actorHash(), "demo", model.DesiredMaintenance, "planned", 600, key)
	if err != nil {
		t.Fatalf("first state=%+v err=%v", first, err)
	}
	second, err := engine.SetDesiredStateWithRequest(ctx, actorHash(), "demo", model.DesiredMaintenance, "planned", 600, key)
	if err != nil {
		t.Fatalf("replay state=%+v err=%v", second, err)
	}
	if second.Generation != first.Generation {
		t.Fatalf("replay generation=%d first=%d", second.Generation, first.Generation)
	}
	if second.Actual != first.Actual || second.Health != first.Health || second.Reason != first.Reason {
		t.Fatalf("replay response differs: first=%+v second=%+v", first, second)
	}

	if _, err := engine.SetDesiredStateWithRequest(ctx, actorHash(), "demo", model.DesiredRunning, "planned", 0, key); !errors.Is(err, store.ErrIdempotency) {
		t.Fatalf("different request err=%v want ErrIdempotency", err)
	}
	otherActor := strings.Repeat("b", 64)
	if _, err := engine.SetDesiredStateWithRequest(ctx, otherActor, "demo", model.DesiredMaintenance, "planned", 600, key); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatalf("different actor err=%v want ErrActorMismatch", err)
	}
}

func TestDirectDesiredStateEndpointIsNotExposed(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	handler := NewServer(engine, engine.store)
	request := httptest.NewRequest(http.MethodPost, "/v1/services/demo/desired-state", nil)
	request.Header.Set(actorHeader, actorHash())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", response.Code, response.Body.String())
	}
}
