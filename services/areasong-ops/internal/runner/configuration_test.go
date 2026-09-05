package runner

import (
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestParseKubernetesManifestSupportsYAMLJSONAndLists(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: areasong-ops
---
{"apiVersion":"v1","kind":"Service","metadata":{"name":"areasong-ops"}}
`
	objects, err := parseKubernetesManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[0].Kind != "Deployment" || objects[1].Kind != "Service" {
		t.Fatalf("objects=%+v", objects)
	}

	list := `apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: one
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: two
`
	objects, err = parseKubernetesManifest(list)
	if err != nil || len(objects) != 2 {
		t.Fatalf("list objects=%+v err=%v", objects, err)
	}
}

func TestValidateKubernetesManifestEnforcesPolicyAndDangerousKinds(t *testing.T) {
	target := model.KubernetesTarget{
		Namespace: "areasong-ops", ResourceKinds: []string{"Deployment", "Service"},
		Allowlist: []string{"deployment/areasong-ops", "service/areasong-ops"},
	}
	valid := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: areasong-ops
  namespace: areasong-ops
`
	if _, err := validateKubernetesManifest(valid, target); err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]string{
		"wrong namespace": strings.Replace(valid, "areasong-ops\n", "other\n", 1),
		"wrong object":    strings.Replace(valid, "areasong-ops\n", "other\n", 1),
		"dangerous kind": `apiVersion: v1
kind: Namespace
metadata:
  name: areasong-ops
`,
	} {
		if _, err := validateKubernetesManifest(manifest, target); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestKubernetesAllowlistAcceptsNamespaceQualifiedEntry(t *testing.T) {
	target := model.KubernetesTarget{
		Namespace: "areasong-ops", ResourceKinds: []string{"Deployment"},
		Allowlist: []string{"areasong-ops/deployment/areasong-ops"},
	}
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: areasong-ops
`
	if _, err := validateKubernetesManifest(manifest, target); err != nil {
		t.Fatal(err)
	}
}

func TestValidateComposeContentRejectsDangerousAndAmbiguousYAML(t *testing.T) {
	valid := `services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
    volumes:
      - app-data:/var/lib/app
volumes:
  app-data: {}
`
	if err := validateComposeContent(valid); err != nil {
		t.Fatalf("valid compose rejected: %v", err)
	}
	anchored := `x-json-log-options: &json-log-options
  driver: json-file
  options:
    max-size: "50m"
    max-file: "5"
services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
    logging: *json-log-options
`
	if err := validateComposeContent(anchored); err != nil {
		t.Fatalf("safe Compose alias rejected: %v", err)
	}
	cases := map[string]string{
		"duplicate key": `services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
    image: example/web@sha256:2222222222222222222222222222222222222222222222222222222222222222
`,
		"multi document": `services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
---
services:
  api:
    image: example/api@sha256:1111111111111111111111111111111111111111111111111111111111111111
`,
		"privileged": `services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
    privileged: true
`,
		"host bind": `services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
    volumes:
      - /etc:/etc
`,
		"plaintext secret": `services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
    environment:
      API_TOKEN: plain-secret
`,
		"merge key": `defaults: &defaults
  image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
services:
  web:
    <<: *defaults
`,
		"cyclic alias": `services:
  web: &web
    environment: *web
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateComposeContent(content); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestAnalyzeComposeChangeAllowsOnlyApplicationImageDigest(t *testing.T) {
	runtime := &model.ComposeServiceRuntime{
		ProjectName: "demo", ApplicationService: "web", ApplicationContainer: "demo-web",
		DependencyServices: []string{"db"}, DependencyContainers: []string{"demo-db"},
	}
	baseline := `name: demo
services:
  web:
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
    ports: ["127.0.0.1:8080:8080"]
    networks: [private]
  db:
    image: example/db@sha256:3333333333333333333333333333333333333333333333333333333333333333
networks:
  private: {}
`
	candidate := strings.Replace(baseline,
		"sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"sha256:2222222222222222222222222222222222222222222222222222222222222222", 1)
	analysis, err := analyzeComposeChange(runtime, baseline, candidate)
	if err != nil || len(analysis.Diff) != 1 || analysis.Diff[0].Path != "services.web.image" {
		t.Fatalf("analysis=%+v err=%v", analysis, err)
	}
	reformatted := `networks:
  private: {}
services:
  db: {image: example/db@sha256:3333333333333333333333333333333333333333333333333333333333333333}
  web:
    networks: [private]
    ports:
      - 127.0.0.1:8080:8080
    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111
name: demo
`
	same, err := analyzeComposeChange(runtime, baseline, reformatted)
	if err != nil || same.BaselineSemanticDigest != same.CandidateSemanticDigest || len(same.Diff) != 0 {
		t.Fatalf("format-only diff=%+v err=%v", same, err)
	}

	cases := map[string]string{
		"project name":     strings.Replace(candidate, "name: demo", "name: escaped", 1),
		"published ports":  strings.Replace(candidate, "8080:8080", "9090:8080", 1),
		"networks":         strings.Replace(candidate, "networks: [private]", "networks: [public]", 1),
		"env file":         strings.Replace(candidate, "ports: [\"127.0.0.1:8080:8080\"]", "ports: [\"127.0.0.1:8080:8080\"]\n    env_file: ../../etc/passwd", 1),
		"host path":        strings.Replace(candidate, "networks: [private]", "networks: [private]\n    volumes: [\"${HOST_PATH}:/data\"]", 1),
		"dependency image": strings.Replace(candidate, "sha256:3333333333333333333333333333333333333333333333333333333333333333", "sha256:4444444444444444444444444444444444444444444444444444444444444444", 1),
		"service add":      candidate + "  extra:\n    image: example/extra@sha256:5555555555555555555555555555555555555555555555555555555555555555\n",
		"mutable image":    strings.Replace(candidate, analysis.CandidateImage, "example/web:latest", 1),
		"image repository": strings.Replace(candidate, "example/web@sha256:", "registry.invalid/escaped@sha256:", 1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := analyzeComposeChange(runtime, baseline, content); err == nil {
				t.Fatalf("%s change was accepted", name)
			}
		})
	}
}
