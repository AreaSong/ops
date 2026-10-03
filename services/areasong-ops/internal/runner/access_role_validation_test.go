package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func versionedRoleRequest(t *testing.T, f *tenantReviewFixture, request model.AccessControlUpdateRequest) model.AccessControlUpdateRequest {
	t.Helper()
	snapshot, found, err := f.db.GetAccessPolicySnapshot(context.Background())
	if err != nil || !found {
		t.Fatalf("snapshot: found=%v err=%v", found, err)
	}
	f.seq++
	request.ExpectedVersion = snapshot.Version
	request.IdempotencyKey = fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
	request.RequiresDualApproval = true
	return request
}

// 只用于合成旧版本持久化记录；不经过待验证的新请求校验。
func saveHistoricalRoleRequest(t *testing.T, f *tenantReviewFixture, request model.AccessControlUpdateRequest) model.AccessChange {
	t.Helper()
	request = versionedRoleRequest(t, f, request)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	change := model.AccessChange{
		ID: request.IdempotencyKey, IdempotencyKey: request.IdempotencyKey,
		RequestDigest: digestText(string(payload)), ActorHash: f.actors["creator"],
		State: model.AccessChangePendingApproval, RequiresDualApproval: true,
		ApprovalPolicy: model.ApprovalPolicyTwoParty, ConfirmationPhrase: "批准历史合成提案",
	}
	stored, created, err := f.db.CreateAccessChange(context.Background(), change, string(payload), store.HashConfirmation(change.ConfirmationPhrase))
	if err != nil || !created {
		t.Fatalf("historical fixture: created=%v err=%v", created, err)
	}
	return stored
}

func assertRoleWriteRejected(t *testing.T, f *tenantReviewFixture, method, path string, request any) {
	t.Helper()
	before := f.durableState(t)
	raw := f.call(t, "creator", method, path, request, 409)
	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body, map[string]string{"error": "自定义角色包含未登记权限"}) &&
		!reflect.DeepEqual(body, map[string]string{"error": "自定义角色必须至少包含一个权限"}) {
		t.Fatalf("unexpected validation error: %s", raw)
	}
	// 此授权成功后的校验分支没有失败审计写入；批准前已产生的审计必须保留。
	if after := f.durableState(t); after != before {
		t.Fatal("rejection changed durable rows, snapshots, receipts, proposal or audits")
	}
}

func TestRoleWriteRejectsUnknownNewRequest(t *testing.T) {
	f := newRoleReviewFixture(t)
	r := versionedRoleRequest(t, f, roleRequest("invalid-role", "Invalid", "*"))
	assertRoleWriteRejected(t, f, "POST", "/v1/access/changes", r)
}

func TestRoleWriteRejectsHistoricalApproved(t *testing.T) {
	f := newRoleReviewFixture(t)
	c := saveHistoricalRoleRequest(t, f, roleRequest("legacy-role", "Legacy", "*"))
	path := "/v1/access/changes/" + c.ID
	f.call(t, "approver", "POST", path+"/approve", model.AccessChangeApprovalRequest{Digest: c.RequestDigest, Confirmation: c.ConfirmationPhrase}, 200)
	assertRoleWriteRejected(t, f, "POST", path+"/apply", nil)
}

func TestRoleWriteInvalidPermissionsAcrossHTTPEntrypoints(t *testing.T) {
	cases := [][]model.Permission{
		nil, {}, {""}, {" "}, {"*"}, {"ops.*"}, {"future"}, {" ops.read"},
		{"ops.read "}, {"ops.read\t"}, {"OPS.READ"}, {"Ops.Read"}, {"ops.read\u00a0"},
		{model.PermissionRead, "*"}, {model.PermissionInspect, "future", model.PermissionRead},
	}
	for _, entry := range []string{"POST", "PUT-approved", "PUT-direct"} {
		for i, permissions := range cases {
			t.Run(fmt.Sprintf("%s/%d", entry, i), func(t *testing.T) {
				f := newRoleReviewFixture(t)
				r := versionedRoleRequest(t, f, roleRequest("invalid-role", "Invalid", permissions...))
				method, path := "PUT", "/v1/access"
				if entry == "POST" {
					method, path = "POST", "/v1/access/changes"
				}
				r.RequiresDualApproval = false // schema 4 仍须强制走创建校验。
				if entry == "PUT-direct" {
					f.engine.catalog.SchemaVersion = 3
				}
				assertRoleWriteRejected(t, f, method, path, r)
			})
		}
	}
}

func TestRoleWriteDecodedPermissionValues(t *testing.T) {
	for _, tc := range []struct {
		name, permissions string
		status            int
	}{
		{"escaped wildcard", `["\u002a"]`, 409},
		{"null item", `[null]`, 409},
		{"null array", `null`, 409},
		{"wrong type", `[7]`, 400},
		{"escaped registered value", `["\u006fps.read"]`, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRoleReviewFixture(t)
			before := f.durableState(t)
			body := `{"roles":[{"id":"decoded","displayName":"Decoded","permissions":` + tc.permissions + `}],"expectedVersion":1,"idempotencyKey":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}`
			req := httptest.NewRequest("POST", "/v1/access/changes", strings.NewReader(body))
			req.Header.Set(actorHeader, f.actors["creator"])
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			f.handler.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if tc.status != 202 && f.durableState(t) != before {
				t.Fatal("decoded rejection wrote state")
			}
			if tc.status == 202 {
				var c model.AccessChange
				if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
					t.Fatal(err)
				}
				f.apply(t, c)
			}
		})
	}
}

// 与前端一致性测试一样直接读取权威常量；新增登记值必须有真实新增/编辑证据。
func declaredRolePermissions(t *testing.T) []model.Permission {
	t.Helper()
	source, err := os.ReadFile("../model/control.go")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`Permission\w+\s+Permission = "([^"]+)"`).FindAllSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatal("permission registry not found")
	}
	permissions := make([]model.Permission, 0, len(matches))
	for _, match := range matches {
		permissions = append(permissions, model.Permission(match[1]))
	}
	return permissions
}

func TestRoleWriteRegisteredPermissions(t *testing.T) {
	permissions := declaredRolePermissions(t)
	for _, permission := range permissions {
		t.Run(string(permission), func(t *testing.T) {
			f := newRoleReviewFixture(t)
			c := submitReviewRequest(t, f, roleRequest(" Registered ", " Original ", permission))
			assertRoleApplied(t, f, c, readRoleDetail(t, f, c))
			c = submitReviewRequest(t, f, roleRequest("registered", " Renamed ", permission, model.PermissionRead, permission))
			detail := readRoleDetail(t, f, c)
			if !reflect.DeepEqual(detail.Role.After.Permissions, []model.Permission{permission, model.PermissionRead, permission}) {
				t.Fatal("detail changed order or duplicates")
			}
			assertRoleApplied(t, f, c, detail)
		})
	}
	f := newRoleReviewFixture(t)
	c := submitReviewRequest(t, f, roleRequest("all-registered", "All", permissions...))
	assertRoleApplied(t, f, c, readRoleDetail(t, f, c))
}
