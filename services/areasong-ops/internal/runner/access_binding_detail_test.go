package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func bindingDetail(t *testing.T, f *tenantReviewFixture, c model.AccessChange) accessChangeDetail {
	t.Helper()
	raw := f.call(t, "approver", "GET", "/v1/access/changes/"+c.ID+"/detail", nil, 200)
	if strings.Contains(string(raw), "@") || strings.Contains(string(raw), `"createdBy"`) || strings.Contains(string(raw), `"principals"`) {
		t.Fatalf("projection leaked: %s", raw)
	}
	var d accessChangeDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Availability != "ready" && d.Binding != nil {
		t.Fatal("partial projection", d)
	}
	return d
}
func basicBinding() model.RoleBinding {
	return model.RoleBinding{ID: "test-binding", Subject: " New-User@EXAMPLE.test ", TenantID: " DEFAULT ", RoleID: " VIEWER "}
}
func TestBindingDetailLifecycle(t *testing.T) {
	f := newTenantReviewFixture(t)
	b := basicBinding()
	expires, _ := time.Parse(time.RFC3339Nano, "2000-01-01T12:00:00.123456789+08:00")
	b.ExpiresAt = &expires
	b.ObjectIDs = []string{"unregistered:two", "unregistered:one", "unregistered:two"}
	c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{b}})
	state := f.durableState(t)
	d := bindingDetail(t, f, c)
	bindingDetail(t, f, c)
	if d.Kind != "binding" || d.Operation != "create" || d.Binding == nil || d.Binding.Before != nil || d.Binding.After.ExpiresAt == nil || *d.Binding.After.ExpiresAt != "2000-01-01T04:00:00.123456789Z" || !reflect.DeepEqual(d.Binding.After.ObjectIDs, b.ObjectIDs) {
		t.Fatal(d)
	}
	if state != f.durableState(t) {
		t.Fatal("GET wrote data")
	}
	for _, actor := range []string{"viewer", "tenant-manager"} {
		f.call(t, actor, "GET", "/v1/access/changes/"+c.ID+"/detail", nil, 403)
	}
	f.apply(t, c)
	b.ExpiresAt = nil
	b.ObjectIDs = nil
	b.RoleID = "platform-admin"
	edit := submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{b}})
	d = bindingDetail(t, f, edit)
	if d.Availability != "ready" || d.Operation != "edit" || d.Binding.Before.ExpiresAt == nil || d.Binding.After.ExpiresAt != nil || len(d.Binding.After.ObjectIDs) != 0 || !reflect.DeepEqual(d.Binding.After.Permissions, []model.Permission{"*"}) || !reflect.DeepEqual(d.Binding.ChangedFields, []string{"roleId", "permissions", "objectIds", "expiresAt"}) {
		t.Fatalf("%+v %+v", d, d.Binding)
	}
	f.apply(t, edit)
	rows, _ := f.db.ListRoleBindings(context.Background())
	if len(rows) != 1 || !reflect.DeepEqual(bindingValue(rows[0], f.engine.catalog.Access), d.Binding.After) {
		t.Fatal("after differs from write", rows, d.Binding)
	}
	revoke := submitReviewRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: []string{b.ID}})
	d = bindingDetail(t, f, revoke)
	if d.Availability != "ready" || d.Operation != "revoke" || d.Binding.Before == nil || d.Binding.After != nil {
		t.Fatal(d)
	}
	f.apply(t, revoke)
	rows, _ = f.db.ListRoleBindings(context.Background())
	if len(rows) != 0 {
		t.Fatal(rows)
	}
}
func TestBindingDetailUnsupported(t *testing.T) {
	mutations := map[string]func(*model.RoleBinding){
		"jit": func(b *model.RoleBinding) { b.JIT = true }, "dual": func(b *model.RoleBinding) { b.RequiresDualApproval = true }, "state": func(b *model.RoleBinding) { b.ApprovalState = "pending" }, "approvedBy": func(b *model.RoleBinding) { b.ApprovedByHash = "x" }, "secondBy": func(b *model.RoleBinding) { b.SecondApprovedByHash = "x" }, "approvedAt": func(b *model.RoleBinding) { v := time.Now(); b.ApprovedAt = &v }, "secondAt": func(b *model.RoleBinding) { v := time.Now(); b.SecondApprovedAt = &v }, "creator": func(b *model.RoleBinding) { b.CreatedBy = "x" }, "createdAt": func(b *model.RoleBinding) { b.CreatedAt = time.Now() }, "updatedAt": func(b *model.RoleBinding) { b.UpdatedAt = time.Now() }, "upperID": func(b *model.RoleBinding) { b.ID = "UPPER" }, "wildTenant": func(b *model.RoleBinding) { b.TenantID = "*" }, "badSubject": func(b *model.RoleBinding) { b.Subject = "not-hash" }, "badRole": func(b *model.RoleBinding) { b.RoleID = "missing" }, "emptyObject": func(b *model.RoleBinding) { b.ObjectIDs = []string{""} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newTenantReviewFixture(t)
			b := basicBinding()
			mutate(&b)
			c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{b}})
			state := f.durableState(t)
			d := bindingDetail(t, f, c)
			if d.Kind != "binding" || d.Availability != "unsupported" {
				t.Fatal(d)
			}
			if state != f.durableState(t) {
				t.Fatal("GET wrote")
			}
		})
	}
	for _, r := range []model.AccessControlUpdateRequest{{Bindings: []model.RoleBinding{basicBinding(), basicBinding()}}, {Bindings: []model.RoleBinding{basicBinding()}, RemoveBindingIDs: []string{"test-binding"}}, {Bindings: []model.RoleBinding{basicBinding()}, Tenants: []model.Tenant{{ID: "mixed"}}}, {RemoveBindingIDs: []string{"missing"}}, {RemoveBindingIDs: []string{"test-binding", "test-binding"}}} {
		f := newTenantReviewFixture(t)
		d := bindingDetail(t, f, submitReviewRequest(t, f, r))
		if d.Kind != "binding" || d.Availability != "unsupported" {
			t.Fatal(d)
		}
	}
}
func TestBindingDetailRangesAndSource(t *testing.T) {
	for _, objects := range [][]string{nil, {}, {"*"}, {"x", "x", "y"}} {
		f := newTenantReviewFixture(t)
		b := basicBinding()
		b.ObjectIDs = objects
		c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{b}})
		d := bindingDetail(t, f, c)
		if d.Availability != "ready" || !reflect.DeepEqual(d.Binding.After.ObjectIDs, append([]string{}, objects...)) {
			t.Fatal(d)
		}
		f.apply(t, c)
		e := submitReviewRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: []string{b.ID}})
		if d = bindingDetail(t, f, e); d.Availability != "ready" {
			t.Fatal(d)
		}
	}
	for name, sql := range map[string]string{"missing": "DELETE FROM role_bindings", "bootstrap": "UPDATE role_bindings SET created_by='bootstrap'", "unknownSource": "UPDATE role_bindings SET created_by=''", "subject": "UPDATE role_bindings SET subject='bad'", "role": "UPDATE role_bindings SET role_id='manager'", "objects": "UPDATE role_bindings SET object_ids_json='[\"different\"]'", "approvalTime": "UPDATE role_bindings SET approved_at='2000-01-01T00:00:00Z'", "alias": "UPDATE role_bindings SET id='TEST-BINDING'"} {
		t.Run(name, func(t *testing.T) {
			f := newTenantReviewFixture(t)
			f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{basicBinding()}}))
			c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: []string{"test-binding"}})
			if _, err := f.sql.Exec(sql); err != nil {
				t.Fatal(err)
			}
			d := bindingDetail(t, f, c)
			if d.Availability != "unsupported" {
				t.Fatal(d)
			}
		})
	}
}
func TestBindingDetailHistoricalIntegrity(t *testing.T) {
	f := newTenantReviewFixture(t)
	b := basicBinding()
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{b}}))
	for _, mutate := range []func(*model.RoleBinding){func(b *model.RoleBinding) { b.Subject = "other@example.test" }, func(b *model.RoleBinding) { b.TenantID = "isolated" }} {
		next := b
		mutate(&next)
		d := bindingDetail(t, f, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{next}}))
		if d.Availability != "unsupported" {
			t.Fatal(d)
		}
	}
	c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: []string{b.ID}})
	f.apply(t, f.proposal(t, "next-tenant", "Next"))
	d := bindingDetail(t, f, c)
	if d.Availability != "stale" {
		t.Fatal(d)
	}
	for _, sql := range []string{"UPDATE access_changes SET request_digest='bad'", "UPDATE access_changes SET payload_json=payload_json || ' '"} {
		if _, err := f.sql.Exec(sql); err != nil {
			t.Fatal(err)
		}
		if d = bindingDetail(t, f, c); d.Availability == "ready" || d.Kind == "other" {
			t.Fatal(d)
		}
	}
}

func TestBindingDetailEnvelopeIntegrity(t *testing.T) {
	cases := []struct {
		name, want string
		mutate     func(*testing.T, *tenantReviewFixture, *model.AccessChange)
	}{
		{"payload digest", "unsupported", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET payload_json=payload_json||' ' WHERE id=?`, c.ID)
		}},
		{"idempotency mismatch", "unsupported", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET idempotency_key='different' WHERE id=?`, c.ID)
		}},
		{"unknown payload field", "unsupported", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewPayload(t, f, c, func(raw string) string { return strings.TrimSuffix(raw, "}") + `,"future":true}` })
		}},
		{"duplicate payload key", "unsupported", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewPayload(t, f, c, func(raw string) string { return strings.TrimSuffix(raw, "}") + `,"expectedVersion":1}` })
		}},
		{"no fixed version", "unavailable", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewPayload(t, f, c, func(raw string) string {
				var r model.AccessControlUpdateRequest
				json.Unmarshal([]byte(raw), &r)
				r.ExpectedVersion = 0
				b, _ := json.Marshal(r)
				return string(b)
			})
		}},
		{"missing snapshot", "unavailable", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewPayload(t, f, c, func(raw string) string {
				return strings.Replace(raw, `"expectedVersion":1`, `"expectedVersion":999`, 1)
			})
		}},
		{"snapshot digest", "unavailable", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_policy_snapshots SET digest='broken' WHERE version=1`)
		}},
		{"unknown snapshot field", "unsupported", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewSnapshot(t, f, func(raw string) string { return strings.TrimSuffix(raw, "}") + `,"future":true}` })
		}},
		{"duplicate snapshot key", "unsupported", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewSnapshot(t, f, func(raw string) string { return strings.TrimSuffix(raw, "}") + `,"enforced":true}` })
		}},
		{"legacy approval", "unsupported", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET approval_policy='' WHERE id=?`, c.ID)
		}},
		{"rejected", "unavailable", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET state='rejected' WHERE id=?`, c.ID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenantReviewFixture(t)
			c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{basicBinding()}})
			tc.mutate(t, f, &c)
			before := f.durableState(t)
			for i := 0; i < 2; i++ {
				d := bindingDetail(t, f, c)
				if d.Availability != tc.want || d.Binding != nil || d.Operation != "" {
					t.Fatalf("%+v", d)
				}
			}
			if f.durableState(t) != before {
				t.Fatal("invalid detail changed persistent state")
			}
		})
	}
}
