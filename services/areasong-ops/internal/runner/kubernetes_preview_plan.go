package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func (engine *Engine) kubernetesPlanReplay(ctx context.Context, actor, key, digest string) (model.KubernetesPlan, bool, error) {
	plan, found, err := engine.store.KubernetesPlanForRequest(ctx, key)
	if err != nil || !found {
		return plan, found, err
	}
	if plan.ActorHash != actor || plan.RequestDigest != digest {
		return model.KubernetesPlan{}, false, store.ErrIdempotency
	}
	return plan, true, nil
}

func (engine *Engine) prepareKubernetesPreview(ctx context.Context, plan *model.KubernetesPlan, manifest, sourceOperation string) error {
	preview, err := collectKubernetesPreview(ctx, plan.Target, manifest)
	if err != nil {
		return err
	}
	preview.SourceOperationID = sourceOperation
	plan.Preview = preview
	if err := engine.store.EnsureKubernetesScopeIdle(ctx, *plan); err != nil {
		return err
	}
	plan.PlanDigest, err = plan.ApprovalDigest()
	if err != nil {
		return err
	}
	prefix := kubernetesApplyConfirmationPrefix
	if plan.Action == "rollback" {
		prefix = kubernetesRollbackConfirmationPrefix
	}
	plan.ConfirmationPhrase = fmt.Sprintf("%s %s/%s %s", prefix, plan.Target.Cluster, plan.Target.Namespace, plan.PlanDigest[:22])
	return nil
}

// 不放宽“成功历史版本”校验；仅单独接受已有完整 rollout_failed 证据的来源。
func (engine *Engine) kubernetesRollbackSource(ctx context.Context, id string) (model.KubernetesPlan, error) {
	plan, err := engine.store.GetKubernetesPlan(ctx, id)
	if err != nil {
		return plan, err
	}
	if plan.Action != "apply" {
		return plan, errors.New("回滚来源必须是 apply 计划")
	}
	if plan.State == "succeeded" {
		return engine.successfulKubernetesPlan(ctx, id)
	}
	if plan.State != "needs_attention" || plan.OperationID == "" || plan.FinishedAt == nil {
		return plan, errors.New("Kubernetes apply 结果未知，不能自动推断回滚资格")
	}
	op, _, err := engine.store.GetKubernetesOperation(ctx, plan.OperationID)
	if err != nil || op.Action != "apply" || op.State != "needs_attention" || op.Phase != "rollout_failed" ||
		op.RolloutState != "failed" || op.FinishedAt == nil || op.ManifestDigest != plan.ManifestDigest ||
		op.TenantID != plan.TenantID || op.Target.Cluster != plan.Target.Cluster || op.Target.Context != plan.Target.Context || op.Target.Namespace != plan.Target.Namespace {
		return plan, errors.New("Kubernetes 缺少 apply 已完成、rollout 失败的完整证据")
	}
	return plan, nil
}

func sameKubernetesObjectSet(source, destination string, target model.KubernetesTarget) error {
	keys := func(manifest string) ([]string, error) {
		objects, err := validateKubernetesManifest(manifest, target)
		if err != nil {
			return nil, err
		}
		result := make([]string, 0, len(objects))
		for _, object := range objects {
			key, _ := kubernetesObjectKeys(object, target.Namespace)
			result = append(result, object.APIVersion+"/"+key)
		}
		sort.Strings(result)
		return result, nil
	}
	left, err := keys(source)
	if err != nil {
		return err
	}
	right, err := keys(destination)
	if err != nil {
		return err
	}
	if !slices.Equal(left, right) {
		return errors.New("Kubernetes 回滚对象集合不一致；不会自动删除或扩大资源范围")
	}
	return nil
}
