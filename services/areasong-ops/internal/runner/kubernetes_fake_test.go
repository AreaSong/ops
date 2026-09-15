package runner

import "strings"

func kubernetesMutationInvocations(log string) []string {
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, " apply ") || strings.Contains(line, " create ") || strings.Contains(line, " rollout ") {
			result = append(result, line)
		}
	}
	return result
}

const kubernetesPreviewFake = `#!/usr/bin/env python3
import json, os, sys, time
args = sys.argv[1:]
with open(os.environ["KUBECTL_INVOCATIONS"], "a") as log:
    log.write(" ".join(args) + "\n")
body = sys.stdin.read()
action = args[4]
if action == "config":
    print(json.dumps({"clusters":[{"name":"cluster-a","cluster":{"server":os.getenv("KUBECTL_SERVER","https://cluster.example.test")}}]}))
elif action == "get":
    if os.getenv("KUBECTL_MISSING") != "1":
        kind, name = args[5].split("/", 1)
        kinds = {"deployment":"Deployment","configmap":"ConfigMap","service":"Service"}
        print(json.dumps({"apiVersion":"apps/v1" if kind=="deployment" else "v1",
            "kind":kinds.get(kind,kind),"metadata":{"name":name,"namespace":args[3],
            "uid":os.getenv("KUBECTL_UID","uid-app"),"resourceVersion":os.getenv("KUBECTL_RV","1")}}))
elif action == "diff":
    code = int(os.getenv("KUBECTL_DIFF_EXIT","1"))
    if code == 1:
        temp = os.getenv("KUBECTL_TEMP","one")
        print("diff -u -N /tmp/LIVE-"+temp+"/object /tmp/MERGED-"+temp+"/object")
        print("--- /tmp/LIVE-"+temp+"/object\n+++ /tmp/MERGED-"+temp+"/object")
        print("@@ -1,7 +1,7 @@\n apiVersion: apps/v1\n kind: Deployment\n metadata:\n   name: app-a\n   namespace: ns-a\n spec:\n-  replicas: 1\n+  replicas: "+os.getenv("KUBECTL_DIFF_VALUE","2"))
    elif code != 0:
        print("diff server failure",file=sys.stderr)
    sys.exit(code)
elif action in ("apply", "create"):
    gate = os.getenv("KUBECTL_MUTATION_GATE")
    if gate and "--dry-run=server" not in args:
        with open(gate + ".started", "w") as marker:
            marker.write("running")
        deadline = time.monotonic() + 60
        while not os.path.exists(gate + ".released"):
            if time.monotonic() > deadline:
                sys.exit("fake kubectl gate timed out")
            time.sleep(0.01)
    if action == "create" and os.getenv("KUBECTL_OBJECT_APPEARED") == "1":
        print("AlreadyExists",file=sys.stderr)
        sys.exit(1)
    if os.getenv("KUBECTL_FAIL_APPLY") == "1":
        print("apply result unknown",file=sys.stderr)
        sys.exit(1)
    if "--dry-run=server" not in args and body.strip().startswith("{"):
        for document in body.split("\n---\n"):
            item=json.loads(document)
            if os.getenv("KUBECTL_MISSING") != "1":
                assert item["metadata"]["uid"] == os.getenv("KUBECTL_UID","uid-app")
                assert item["metadata"]["resourceVersion"] == os.getenv("KUBECTL_RV","1")
    print("applied")
elif action == "rollout":
    if os.getenv("KUBECTL_FAIL_ROLLOUT") == "1":
        print("rollout failed",file=sys.stderr)
        sys.exit(1)
    print("rollout complete")
else:
    print("unexpected kubectl action: "+action,file=sys.stderr)
    sys.exit(2)
`
