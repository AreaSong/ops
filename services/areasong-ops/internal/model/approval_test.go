package model

import "testing"

func TestTwoPartyExecutorRequiresIndependentApproval(t *testing.T) {
	creator := "creator"
	approver := "approver"
	base := ReleasePlan{
		Risk:                 RiskHigh,
		ApprovalPolicy:       ApprovalPolicyTwoParty,
		RequiresDualApproval: true,
		ActorHash:            creator,
	}

	for name, plan := range map[string]ReleasePlan{
		"missing approver": base,
		"creator approved self": func() ReleasePlan {
			value := base
			value.ApprovedByHash = creator
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if plan.AllowsExecutor(creator) {
				t.Fatal("malformed two-party plan allowed creator execution")
			}
		})
	}

	base.ApprovedByHash = approver
	if !base.AllowsExecutor(creator) {
		t.Fatal("independently approved two-party plan rejected creator execution")
	}
	if base.AllowsExecutor(approver) {
		t.Fatal("approver was allowed to execute two-party plan")
	}
}
