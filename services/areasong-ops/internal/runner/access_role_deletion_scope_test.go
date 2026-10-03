package runner

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestRoleDeletionRequestScope(t *testing.T) {
	cases := []struct {
		name   string
		change func(*model.AccessControlUpdateRequest)
	}{
		{"multiple", func(r *model.AccessControlUpdateRequest) { r.RemoveRoleIDs = []string{"custom", "another"} }},
		{"duplicate", func(r *model.AccessControlUpdateRequest) { r.RemoveRoleIDs = []string{"custom", "custom"} }},
		{"ambiguous duplicate", func(r *model.AccessControlUpdateRequest) { r.RemoveRoleIDs = []string{"custom", " CUSTOM "} }},
		{"empty extra", func(r *model.AccessControlUpdateRequest) { r.RemoveRoleIDs = []string{"custom", ""} }},
		{"upsert same ID", func(r *model.AccessControlUpdateRequest) {
			r.Roles = roleRequest("custom", "Recreated", model.PermissionRead).Roles
		}},
		{"rename ID", func(r *model.AccessControlUpdateRequest) {
			r.Roles = roleRequest("another", "Another", model.PermissionRead).Roles
		}},
		{"tenant", func(r *model.AccessControlUpdateRequest) { r.Tenants = []model.Tenant{{ID: "x"}} }},
		{"binding", func(r *model.AccessControlUpdateRequest) { r.Bindings = []model.RoleBinding{{ID: "x"}} }},
		{"principal", func(r *model.AccessControlUpdateRequest) { r.Principals = []model.AccessPrincipal{{Subject: "x"}} }},
		{"remove tenant", func(r *model.AccessControlUpdateRequest) { r.RemoveTenantIDs = []string{"x"} }},
		{"remove binding", func(r *model.AccessControlUpdateRequest) { r.RemoveBindingIDs = []string{"x"} }},
		{"remove principal", func(r *model.AccessControlUpdateRequest) { r.RemovePrincipalSubjects = []string{"x"} }},
		{"switch", func(r *model.AccessControlUpdateRequest) { yes := true; r.Enforced = &yes }},
		{"confirmation", func(r *model.AccessControlUpdateRequest) { r.Confirmation = "legacy" }},
	}
	f := deletionFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := deletionRequest("custom")
			tc.change(&r)
			c := submitReviewRequest(t, f, r)
			reason := ""
			if tc.name == "binding" || tc.name == "remove binding" {
				reason = "unsupported_binding_request"
			}
			d := assertDeletionReadOnly(t, f, c, "unsupported", reason)
			kind := "unsupported"
			if reason != "" {
				kind = "binding"
			}
			if d.Kind != kind {
				t.Fatal(d)
			}
		})
	}
	for _, id := range []string{"", " ", " CUSTOM ", "Custom", "cus tom", "custom/other", "*", "自定义"} {
		t.Run("ID="+id, func(t *testing.T) {
			c := submitReviewRequest(t, f, deletionRequest(id))
			assertDeletionReadOnly(t, f, c, "unsupported", "invalid_role_id")
		})
	}
	c := submitReviewRequest(t, f, deletionRequest("missing"))
	assertDeletionReadOnly(t, f, c, "unsupported", "role_not_found")
}

func TestRoleDeletionHistoricalScope(t *testing.T) {
	cases := []struct {
		name, reason string
		modify       func(*config.AccessPolicy)
	}{
		{"builtIn", "protected_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.BuiltIn = true; p.Roles["custom"] = r }},
		{"bootstrap", "protected_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.CreatedBy = "bootstrap"; p.Roles["custom"] = r }},
		{"no provenance", "protected_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.CreatedBy = ""; p.Roles["custom"] = r }},
		{"unknown provenance", "protected_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.CreatedBy = "legacy"; p.Roles["custom"] = r }},
		{"ID mismatch", "incompatible_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.ID = "other"; p.Roles["custom"] = r }},
		{"alias", "incompatible_role", func(p *config.AccessPolicy) { p.Roles[" CUSTOM "] = p.Roles["custom"] }},
		{"empty name", "incompatible_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.DisplayName = " "; p.Roles["custom"] = r }},
		{"unknown permissions", "incompatible_role", func(p *config.AccessPolicy) {
			r := p.Roles["custom"]
			r.Permissions = []model.Permission{"future"}
			p.Roles["custom"] = r
		}},
		{"wildcard permissions", "incompatible_role", func(p *config.AccessPolicy) {
			r := p.Roles["custom"]
			r.Permissions = []model.Permission{"*"}
			p.Roles["custom"] = r
		}},
		{"no permissions", "incompatible_role", func(p *config.AccessPolicy) { r := p.Roles["custom"]; r.Permissions = nil; p.Roles["custom"] = r }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := deletionFixture(t)
			mutateReviewBaseline(t, f, tc.modify)
			c := submitReviewRequest(t, f, deletionRequest("custom"))
			assertDeletionReadOnly(t, f, c, "unsupported", tc.reason)
		})
	}
}

func TestRoleDeletionReferenceScope(t *testing.T) {
	for _, mode := range []string{"ordinary", "expired", "wildcard tenant", "missing tenant", "duplicate binding", "empty binding ID", "bad subject", "ambiguous role ID", "principal", "disabled principal", "principal alias", "malformed principal", "unrelated damaged binding", "disabled tenant", "unknown tenant status"} {
		t.Run(mode, func(t *testing.T) {
			f := deletionFixture(t)
			reason := "unsupported_reference_scope"
			mutateReviewBaseline(t, f, func(p *config.AccessPolicy) {
				if mode == "disabled tenant" || mode == "unknown tenant status" {
					tenant := p.Tenants["isolated"]
					tenant.Status = "disabled"
					if mode == "unknown tenant status" {
						tenant.Status = "future"
					}
					p.Tenants["isolated"] = tenant
					return
				}
				if strings.Contains(mode, "principal") {
					v := p.Principals[f.actors["viewer"]]
					v.Roles = []string{"custom"}
					switch mode {
					case "principal":
						reason = "principal_reference"
					case "disabled principal":
						v.Status = "disabled"
						reason = "principal_reference"
					case "principal alias":
						v.Roles = []string{" CUSTOM "}
					case "malformed principal":
						v.Subject = "bad"
					}
					p.Principals[f.actors["viewer"]] = v
					return
				}
				b := model.RoleBinding{ID: "reference", Subject: f.actors["viewer"], TenantID: "default", RoleID: "custom"}
				switch mode {
				case "ordinary":
					reason = "binding_reference"
				case "expired":
					expiry := time.Now().Add(-time.Hour).UTC()
					b.ExpiresAt = &expiry
					reason = "binding_reference"
				case "wildcard tenant":
					b.TenantID = "*"
				case "missing tenant":
					b.TenantID = "missing"
				case "duplicate binding":
					p.Bindings = append(p.Bindings, b)
				case "empty binding ID":
					b.ID = ""
				case "bad subject":
					b.Subject = "broken"
				case "ambiguous role ID":
					b.RoleID = " CUSTOM "
				case "unrelated damaged binding":
					b.RoleID = "missing"
				}
				p.Bindings = append(p.Bindings, b)
			})
			c := submitReviewRequest(t, f, deletionRequest("custom"))
			assertDeletionReadOnly(t, f, c, "unsupported", reason)
		})
	}
}

func TestRoleDeletionDetailIntegrity(t *testing.T) {
	cases := []struct {
		name, availability, reason string
		mutate                     func(*testing.T, *tenantReviewFixture, *model.AccessChange)
	}{
		{"payload digest", "unsupported", "", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET payload_json=payload_json||' ' WHERE id=?`, c.ID)
		}},
		{"key mismatch", "unsupported", "", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET idempotency_key='other' WHERE id=?`, c.ID)
		}},
		{"unknown payload field", "unsupported", "", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewPayload(t, f, c, func(s string) string { return strings.TrimSuffix(s, "}") + `,"future":true}` })
		}},
		{"duplicate payload key", "unsupported", "", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewPayload(t, f, c, func(s string) string { return strings.TrimSuffix(s, "}") + `,"removeRoleIds":["custom"]}` })
		}},
		{"snapshot digest", "unavailable", "", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_policy_snapshots SET digest='bad' WHERE version=2`)
		}},
		{"unknown baseline field", "unsupported", "incompatible_baseline", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewSnapshot(t, f, func(s string) string { return strings.TrimSuffix(s, "}") + `,"future":true}` })
		}},
		{"duplicate baseline key", "unsupported", "incompatible_baseline", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			rewriteReviewSnapshot(t, f, func(s string) string { return strings.TrimSuffix(s, "}") + `,"enforced":true}` })
		}},
		{"legacy", "unsupported", "incompatible_approval", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET approval_policy='' WHERE id=?`, c.ID)
		}},
		{"no dual approval", "unsupported", "incompatible_approval", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET requires_dual_approval=0 WHERE id=?`, c.ID)
		}},
		{"bad creator", "unsupported", "incompatible_approval", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET actor_hash='bad' WHERE id=?`, c.ID)
		}},
		{"rejected", "unavailable", "", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET state='rejected' WHERE id=?`, c.ID)
		}},
		{"applied same version", "unavailable", "", func(t *testing.T, f *tenantReviewFixture, c *model.AccessChange) {
			execReviewSQL(t, f, `UPDATE access_changes SET state='applied' WHERE id=?`, c.ID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := deletionFixture(t)
			c := submitReviewRequest(t, f, deletionRequest("custom"))
			tc.mutate(t, f, &c)
			assertDeletionReadOnly(t, f, c, tc.availability, tc.reason)
		})
	}
	for _, version := range []int64{0, -1, 999} {
		t.Run("version", func(t *testing.T) {
			f := deletionFixture(t)
			c := submitReviewRequest(t, f, deletionRequest("custom"))
			rewriteReviewPayload(t, f, &c, func(s string) string {
				var r model.AccessControlUpdateRequest
				json.Unmarshal([]byte(s), &r)
				r.ExpectedVersion = version
				b, _ := json.Marshal(r)
				return string(b)
			})
			assertDeletionReadOnly(t, f, c, "unavailable", "")
		})
	}
}
