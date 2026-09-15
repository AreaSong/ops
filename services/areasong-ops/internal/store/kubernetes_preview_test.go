package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestKubernetesStoreRejectsLegacyAndExpiredPreview(t *testing.T) {
	for _, invalid := range []string{"legacy", "expired"} {
		for _, action := range []string{"approve", "execute"} {
			t.Run(invalid+"/"+action, func(t *testing.T) {
				database := openTestStore(t)
				ctx := context.Background()
				now := time.Now().UTC()
				database.now = func() time.Time { return now }
				plan, manifest := atomicKubernetesPlan(now)
				if action == "execute" {
					plan.State = "approved"
					plan.ApprovedByHash, plan.SecondApprovedByHash = "actor-b", "actor-c"
				}
				if invalid == "legacy" {
					plan.Preview, plan.PlanDigest = nil, plan.ManifestDigest
				} else {
					plan.Preview.ObservedAt = now.Add(-16 * time.Minute)
					plan.Preview.ExpiresAt = now.Add(-time.Minute)
					var err error
					plan.PlanDigest, err = plan.ApprovalDigest()
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := database.CreateKubernetesPlan(ctx, plan, manifest, HashConfirmation(plan.ConfirmationPhrase)); err != nil {
					t.Fatal(err)
				}
				assertKubernetesInvalidPreviewRefused(t, database, plan, action)
			})
		}
	}
}

func assertKubernetesInvalidPreviewRefused(t *testing.T, database *Store, plan model.KubernetesPlan, action string) {
	t.Helper()
	ctx := context.Background()
	var err error
	if action == "approve" {
		_, err = database.ApproveKubernetesPlan(ctx, plan.ID, "actor-b", plan.PlanDigest, plan.ConfirmationPhrase)
	} else {
		_, _, err = database.StartKubernetesPlan(ctx, plan.ID, "actor-d", "execute-key", plan.PlanDigest, atomicKubernetesOperation(plan))
	}
	if err == nil {
		t.Fatal("Store 接受了失效预览")
	}
	assertKubernetesPlanState(t, database, plan.ID, nil, plan.State)
	if _, _, err := database.GetKubernetesOperation(ctx, "plan-"+plan.ID); err != ErrNotFound {
		t.Fatalf("失效预览产生了操作: %v", err)
	}
	entries, err := database.ListAudit(ctx, 20, 0)
	if err != nil || len(entries) != 1 || entries[0].Event != "kubernetes.plan.created" {
		t.Fatalf("拒绝操作产生了成功审计: %+v err=%v", entries, err)
	}
}
