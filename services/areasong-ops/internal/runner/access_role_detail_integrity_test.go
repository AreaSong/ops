package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestRoleChangeDetailIntegrity(t *testing.T) {
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
			f := newRoleReviewFixture(t)
			c := submitReviewRequest(t, f, roleRequest("custom", "Custom", model.PermissionRead))
			tc.mutate(t, f, &c)
			before := f.durableState(t)
			for i := 0; i < 2; i++ {
				d := readRoleDetail(t, f, c)
				if d.Availability != tc.want || d.Role != nil || d.Operation != "" {
					t.Fatalf("%+v", d)
				}
			}
			if f.durableState(t) != before {
				t.Fatal("invalid detail changed persistent state")
			}
		})
	}
}

func execReviewSQL(t *testing.T, f *tenantReviewFixture, query string, args ...any) {
	t.Helper()
	if _, err := f.sql.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func rewriteReviewPayload(t *testing.T, f *tenantReviewFixture, c *model.AccessChange, modify func(string) string) {
	t.Helper()
	_, raw, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw = modify(raw)
	c.RequestDigest = digestText(raw)
	execReviewSQL(t, f, `UPDATE access_changes SET payload_json=?,request_digest=? WHERE id=?`, raw, c.RequestDigest, c.ID)
}
func rewriteReviewSnapshot(t *testing.T, f *tenantReviewFixture, modify func(string) string) {
	t.Helper()
	snapshot, _, err := f.db.GetAccessPolicySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := modify(snapshot.PolicyJSON)
	execReviewSQL(t, f, `UPDATE access_policy_snapshots SET policy_json=?,digest=? WHERE version=?`, raw, digestText(raw), snapshot.Version)
}

func TestRoleChangeDetailHistoricalScope(t *testing.T) {
	cases := []struct {
		name, reason string
		modify       func(*config.AccessPolicy)
	}{
		{"built in historical", "protected_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.BuiltIn = true; p.Roles["custom"] = r }},
		{"unknown provenance", "protected_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.CreatedBy = ""; p.Roles["custom"] = r }},
		{"role ID mismatch", "incompatible_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.ID = "different"; p.Roles["custom"] = r }},
		{"unknown old permission", "incompatible_role", func(p *config.AccessPolicy) {
			r := p.Roles["custom"]
			r.Permissions = []model.Permission{"future"}
			p.Roles["custom"] = r
		}},
		{"wildcard old permission", "incompatible_role", func(p *config.AccessPolicy) {
			r := p.Roles["custom"]
			r.Permissions = []model.Permission{"*"}
			p.Roles["custom"] = r
		}},
		{"principal reference", "unsupported_reference_scope", func(p *config.AccessPolicy) {
			p.Principals["private-subject"] = config.AccessPrincipal{TenantID: "default", Roles: []string{"custom"}}
		}},
		{"wildcard tenant reference", "unsupported_reference_scope", func(p *config.AccessPolicy) {
			p.Bindings = append(p.Bindings, model.RoleBinding{ID: "private", RoleID: "custom", TenantID: "*"})
		}},
		{"unknown tenant reference", "unsupported_reference_scope", func(p *config.AccessPolicy) {
			p.Bindings = append(p.Bindings, model.RoleBinding{ID: "private", RoleID: "custom", TenantID: "unknown"})
		}},
		{"duplicate binding ID", "unsupported_reference_scope", func(p *config.AccessPolicy) {
			b := model.RoleBinding{ID: "private", RoleID: "custom", TenantID: "default"}
			p.Bindings = append(p.Bindings, b, b)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRoleReviewFixture(t)
			c := submitReviewRequest(t, f, roleRequest("custom", "Custom", model.PermissionRead))
			f.apply(t, c)
			mutateReviewBaseline(t, f, tc.modify)
			edit := submitReviewRequest(t, f, roleRequest("custom", "Edited", model.PermissionRead))
			before := f.durableState(t)
			d := readRoleDetail(t, f, edit)
			if d.Availability != "unsupported" || d.Reason != tc.reason || d.Role != nil {
				t.Fatalf("%+v", d)
			}
			if before != f.durableState(t) {
				t.Fatal("historical scope GET wrote state")
			}
		})
	}
}

func TestRoleChangeDetailBuiltInApplicationProtection(t *testing.T) {
	f := newRoleReviewFixture(t)
	for _, id := range []string{"viewer", "operator", "release-manager", "platform-admin", "manager"} {
		change := submitReviewRequest(t, f, roleRequest(id, "Replacement", model.PermissionRead))
		detail := readRoleDetail(t, f, change)
		if detail.Role != nil || detail.Availability != "unsupported" {
			t.Fatal(detail)
		}
		path := "/v1/access/changes/" + change.ID
		f.call(t, "approver", "POST", path+"/approve", model.AccessChangeApprovalRequest{Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase}, 200)
		before := f.durableState(t)
		f.call(t, "creator", "POST", path+"/apply", nil, 409)
		if before != f.durableState(t) {
			t.Fatal("protected role application mutated state")
		}
	}
}
