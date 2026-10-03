package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestRoleProposalIdentityAndReplay(t *testing.T) {
	f := newRoleReviewFixture(t)
	before, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	change := submitReviewRequest(t, f, roleRequest("identity-role", "Role", model.PermissionRead))
	_, payload, err := f.db.GetAccessChangeWithPayload(context.Background(), change.ID)
	if err != nil {
		t.Fatal(err)
	}
	var request model.AccessControlUpdateRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatal(err)
	}
	raw := f.call(t, "creator", "POST", "/v1/access/changes", request, 200)
	var replay model.AccessChange
	if err := json.Unmarshal(raw, &replay); err != nil || replay.ID != change.ID {
		t.Fatal("duplicate proposal", err)
	}
	request.Roles[0].DisplayName = "Different payload"
	f.call(t, "creator", "POST", "/v1/access/changes", request, 409)
	f.call(t, "viewer", "POST", "/v1/access/changes", request, 403)
	path := "/v1/access/changes/" + change.ID
	approval := model.AccessChangeApprovalRequest{Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase}
	f.call(t, "creator", "POST", path+"/approve", approval, 409)
	f.call(t, "viewer", "POST", path+"/approve", approval, 403)
	f.call(t, "approver", "POST", path+"/approve", model.AccessChangeApprovalRequest{Digest: "wrong", Confirmation: change.ConfirmationPhrase}, 409)
	f.call(t, "approver", "POST", path+"/approve", approval, 200)
	f.call(t, "approver", "POST", path+"/approve", approval, 200)
	f.call(t, "approver", "POST", path+"/apply", nil, 409)
	f.call(t, "other", "POST", path+"/apply", nil, 409)
	f.call(t, "viewer", "POST", path+"/apply", nil, 403)
	f.call(t, "creator", "POST", path+"/apply", nil, 200)
	first := f.durableState(t)
	f.call(t, "creator", "POST", path+"/apply", nil, 200)
	if f.durableState(t) != first {
		t.Fatal("apply replay changed durable state")
	}
	after, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Bindings, after.Bindings) || !reflect.DeepEqual(before.Tenants, after.Tenants) || !reflect.DeepEqual(before.Principals, after.Principals) {
		t.Fatal("unrelated policy changed")
	}
	rejected := submitReviewRequest(t, f, roleRequest("cancel-role", "Cancel", model.PermissionRead))
	f.call(t, "creator", "POST", "/v1/access/changes/"+rejected.ID+"/reject", map[string]string{"reason": "cancel"}, 200)
	_, payload, _ = f.db.GetAccessChangeWithPayload(context.Background(), rejected.ID)
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatal(err)
	}
	raw = f.call(t, "creator", "POST", "/v1/access/changes", request, 200)
	if err := json.Unmarshal(raw, &replay); err != nil || replay.State != model.AccessChangeRejected {
		t.Fatal("rejected replay lost state", err)
	}
	rebuilt := submitReviewRequest(t, f, roleRequest("cancel-role", "Cancel", model.PermissionRead))
	if rebuilt.ID == rejected.ID || rebuilt.State != model.AccessChangePendingApproval {
		t.Fatal("explicit rebuild failed")
	}
}
