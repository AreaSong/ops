package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) KubernetesPlanForRequest(ctx context.Context, key string) (model.KubernetesPlan, bool, error) {
	plan, _, _, found, err := kubernetesPlanByKey(ctx, store.db, key)
	return plan, found, err
}

type kubernetesScopeQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func ensureKubernetesScopeIdle(ctx context.Context, query kubernetesScopeQuerier, plan model.KubernetesPlan) error {
	rows, err := query.QueryContext(ctx, kubernetesPlanSelect+` WHERE state='running' AND id<>?`, plan.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		other, _, err := scanKubernetesPlan(rows)
		if err != nil {
			return err
		}
		sameCluster := other.Target.Context == plan.Target.Context
		if plan.Preview != nil && other.Preview != nil {
			sameCluster = sameCluster || other.Preview.ClusterFingerprint == plan.Preview.ClusterFingerprint
		}
		if sameCluster && other.Target.Namespace == plan.Target.Namespace {
			return errors.New("Kubernetes namespace 已有在途计划，先核对其结果")
		}
	}
	return rows.Err()
}

func (store *Store) EnsureKubernetesScopeIdle(ctx context.Context, plan model.KubernetesPlan) error {
	return ensureKubernetesScopeIdle(ctx, store.db, plan)
}
