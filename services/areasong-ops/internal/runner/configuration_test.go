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
    image: example/web:v1
    volumes:
      - app-data:/var/lib/app
volumes:
  app-data: {}
`
	if err := validateComposeContent(valid); err != nil {
		t.Fatalf("valid compose rejected: %v", err)
	}
	cases := map[string]string{
		"duplicate key": `services:
  web:
    image: example/web:v1
    image: example/web:v2
`,
		"multi document": `services:
  web:
    image: example/web:v1
---
services:
  api:
    image: example/api:v1
`,
		"privileged": `services:
  web:
    image: example/web:v1
    privileged: true
`,
		"host bind": `services:
  web:
    image: example/web:v1
    volumes:
      - /etc:/etc
`,
		"plaintext secret": `services:
  web:
    image: example/web:v1
    environment:
      API_TOKEN: plain-secret
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
