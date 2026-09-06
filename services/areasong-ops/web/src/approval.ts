interface ApprovalEnvelope {
  actorHash?: string;
  approvedByHash?: string;
  secondApprovedByHash?: string;
  approvalPolicy?: string;
}

interface ReleasePlanApprovalEnvelope extends ApprovalEnvelope {
  risk?: string;
  service?: string;
  action?: string;
  approvalException?: string;
}

export const twoPartyApprovalPolicy = "two_party_v1";

export function canCurrentActorApprove(
  value: ApprovalEnvelope,
  currentActorHash?: string,
): boolean {
  if (!currentActorHash || !value.actorHash) return false;
  if (currentActorHash === value.actorHash) return false;
  if (currentActorHash === value.approvedByHash) return false;
  if (currentActorHash === value.secondApprovedByHash) return false;
  if (value.approvalPolicy === twoPartyApprovalPolicy) {
    return !value.approvedByHash;
  }
  return true;
}

export function canCurrentActorExecute(
  value: ApprovalEnvelope,
  currentActorHash?: string,
): boolean {
  if (!currentActorHash || !value.actorHash || !value.approvedByHash) {
    return false;
  }
  if (value.approvalPolicy === twoPartyApprovalPolicy) {
    return (
      currentActorHash === value.actorHash &&
      currentActorHash !== value.approvedByHash
    );
  }
  return (
    Boolean(value.secondApprovedByHash) &&
    currentActorHash !== value.actorHash &&
    currentActorHash !== value.approvedByHash &&
    currentActorHash !== value.secondApprovedByHash
  );
}

const c2LifecycleException = "c2_lifecycle_single_actor";

export function canCurrentActorApproveReleasePlan(
  value: ReleasePlanApprovalEnvelope,
  currentActorHash?: string,
): boolean {
  if (value.risk !== "high") return Boolean(currentActorHash);
  if (
    value.approvalException === c2LifecycleException &&
    value.service === "areaforge" &&
    (value.action === "start" || value.action === "stop")
  ) {
    return Boolean(currentActorHash && currentActorHash === value.actorHash);
  }
  return canCurrentActorApprove(value, currentActorHash);
}

export function canCurrentActorExecuteReleasePlan(
  value: ReleasePlanApprovalEnvelope,
  currentActorHash?: string,
): boolean {
  if (value.risk !== "high") {
    return Boolean(currentActorHash && currentActorHash === value.actorHash);
  }
  if (
    value.approvalException === c2LifecycleException &&
    value.service === "areaforge" &&
    (value.action === "start" || value.action === "stop")
  ) {
    return Boolean(
      currentActorHash &&
        currentActorHash === value.actorHash &&
        value.approvedByHash,
    );
  }
  return canCurrentActorExecute(value, currentActorHash);
}
