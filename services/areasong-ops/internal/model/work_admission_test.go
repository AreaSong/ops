package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestWorkAdmissionCanonicalContract(t *testing.T) {
	input := WorkAdmissionRequest{Version: 1, Kind: WorkAdmissionKind, WorkID: "plan", IdempotencyKey: "key", ActorHash: strings.Repeat("a", 64), ApprovalDigest: strings.Repeat("b", 64), Targets: []WorkAdmissionTarget{{"z", 2}, {"a", 1}, {"z", 2}}}
	before := append([]WorkAdmissionTarget(nil), input.Targets...)
	value, raw, digest, err := CanonicalWorkRequest(input)
	if err != nil || len(value.Targets) != 2 || value.Targets[0].TenantID != "a" || digest != WorkDigest(raw) || !reflect.DeepEqual(before, input.Targets) {
		t.Fatalf("%+v %v", value, err)
	}
	for _, targets := range [][]WorkAdmissionTarget{nil, {{"a", 0}}, {{"a", -1}}, {{"*", 1}}, {{" A", 1}}, {{"A", 1}}, {{"a", 1}, {"a", 2}}} {
		input.Targets = targets
		if _, _, _, err := CanonicalWorkRequest(input); err == nil {
			t.Fatalf("accepted %+v", targets)
		}
	}
	token, err := NewWorkOwnerToken()
	if err != nil || !ValidWorkDigest(token) {
		t.Fatal(err)
	}
	token2, _ := NewWorkOwnerToken()
	if token == token2 {
		t.Fatal("reused token")
	}
}
func TestWorkAdmissionEvidenceIsExplicit(t *testing.T) {
	if (WorkCloseEvidence{}).Validate("settled") == nil {
		t.Fatal("empty evidence accepted")
	}
	if (WorkCloseEvidence{Kind: "never_started_v1"}).Validate("no_work") != nil {
		t.Fatal("no-work rejected")
	}
	evidence := WorkCloseEvidence{Kind: "execution_and_cleanup_settled_v1", ExecutorStopped: true, CleanupConfirmed: true, ResultDigest: strings.Repeat("a", 64), CleanupDigest: strings.Repeat("b", 64)}
	if evidence.Validate("settled") != nil {
		t.Fatal("settled rejected")
	}
	evidence.CleanupConfirmed = false
	if evidence.Validate("settled") == nil {
		t.Fatal("cleanup omitted")
	}
	raw, _ := json.Marshal(WorkAdmissionRequest{})
	if strings.Contains(string(raw), "owner") {
		t.Fatal("owner leaked into request")
	}
}
