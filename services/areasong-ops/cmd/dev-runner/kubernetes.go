package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

const developmentKubectlMarker = "# AreaSong Ops development kubectl"

func configureDevelopmentKubernetes(features map[string]bool, runtimeRoot string) error {
	if !features["kubernetes"] {
		return nil
	}
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) == "/" {
		return errors.New("开发 Kubernetes 根目录无效")
	}
	binDir := filepath.Join(runtimeRoot, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(binDir, "kubectl")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("开发 kubectl 不是普通文件")
		}
		previous, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		legacy := "#!/bin/sh\ncat >/dev/null\nprintf '{\"kind\":\"Status\",\"status\":\"Success\"}\\n'\n"
		if !bytes.Contains(previous, []byte(developmentKubectlMarker)) && string(previous) != legacy {
			return errors.New("拒绝覆盖非本工具生成的 kubectl")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	content := "#!/bin/sh\n" + developmentKubectlMarker + "\nexec " + quoted + " --development-kubectl \"$@\"\n"
	if err := atomicDevelopmentFile(path, []byte(content), 0o700); err != nil {
		return err
	}
	if err := os.Setenv("OPS_DEV_KUBE_STATE", filepath.Join(runtimeRoot, "kubernetes-state.json")); err != nil {
		return err
	}
	return os.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func atomicDevelopmentFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".development-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return os.Rename(file.Name(), path)
}

func developmentKubectl(arguments []string, input io.Reader, output io.Writer) (int, error) {
	if len(arguments) < 5 || arguments[0] != "--context" || arguments[2] != "-n" {
		return 2, errors.New("开发 kubectl 需要显式 context/namespace")
	}
	statePath := os.Getenv("OPS_DEV_KUBE_STATE")
	if !filepath.IsAbs(statePath) {
		return 2, errors.New("开发 Kubernetes 状态路径缺失")
	}
	lock, err := os.OpenFile(statePath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 2, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return 2, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := make(map[string]map[string]any)
	if data, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return 2, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 2, err
	}
	namespace, action := arguments[3], arguments[4]
	if action == "config" {
		return 0, json.NewEncoder(output).Encode(map[string]any{"clusters": []any{map[string]any{
			"name": arguments[1], "cluster": map[string]any{"server": "https://development.invalid/" + arguments[1]},
		}}})
	}
	if action == "get" && len(arguments) > 5 {
		if object := state[namespace+"/"+arguments[5]]; object != nil {
			return 0, json.NewEncoder(output).Encode(object)
		}
		return 0, nil
	}
	if action == "rollout" {
		if os.Getenv("OPS_DEV_KUBE_FAIL_ROLLOUT") == "1" {
			return 1, errors.New("development rollout failed")
		}
		_, err := fmt.Fprintln(output, "development rollout complete")
		return 0, err
	}
	if action != "apply" && action != "create" && action != "diff" {
		return 2, errors.New("未支持的开发 Kubernetes 操作")
	}
	documents, err := developmentKubernetesDocuments(input)
	if err != nil {
		return 2, err
	}
	if action == "diff" {
		return developmentKubernetesDiff(state, documents, namespace, output)
	}
	for _, argument := range arguments {
		if argument == "--dry-run=server" {
			return 0, nil
		}
	}
	for _, object := range documents {
		if action == "create" {
			key, _, err := developmentKubernetesKey(object, namespace)
			if err != nil {
				return 2, err
			}
			if state[key] != nil {
				return 1, errors.New("development AlreadyExists")
			}
		}
		if err := applyDevelopmentKubernetesObject(state, object, namespace); err != nil {
			return 1, err
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return 2, err
	}
	if err := atomicDevelopmentFile(statePath, data, 0o600); err != nil {
		return 2, err
	}
	_, err = fmt.Fprintln(output, "development apply complete")
	return 0, err
}

func developmentKubernetesDocuments(input io.Reader) ([]map[string]any, error) {
	decoder := yaml.NewDecoder(io.LimitReader(input, 1024*1024))
	var objects []map[string]any
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(document) == 0 {
			continue
		}
		kind, _ := document["kind"].(string)
		if strings.HasSuffix(strings.ToLower(kind), "list") {
			items, ok := document["items"].([]any)
			if !ok {
				return nil, errors.New("开发 List 缺少对象")
			}
			for _, item := range items {
				value, ok := item.(map[string]any)
				if !ok {
					return nil, errors.New("开发 List 项无效")
				}
				objects = append(objects, value)
			}
		} else {
			objects = append(objects, document)
		}
	}
	return objects, nil
}

func developmentKubernetesKey(object map[string]any, namespace string) (string, map[string]any, error) {
	meta, ok := object["metadata"].(map[string]any)
	if !ok {
		return "", nil, errors.New("开发对象缺少 metadata")
	}
	name, _ := meta["name"].(string)
	kind, _ := object["kind"].(string)
	if name == "" || kind == "" {
		return "", nil, errors.New("开发对象缺少身份")
	}
	meta["namespace"] = namespace
	return namespace + "/" + strings.ToLower(kind) + "/" + name, meta, nil
}

func developmentKubernetesComparable(object map[string]any) []byte {
	if object == nil {
		return []byte("{}")
	}
	raw, _ := json.Marshal(object)
	var clone map[string]any
	_ = json.Unmarshal(raw, &clone)
	if meta, ok := clone["metadata"].(map[string]any); ok {
		delete(meta, "uid")
		delete(meta, "resourceVersion")
	}
	data, _ := json.MarshalIndent(clone, "", "  ")
	return data
}

func developmentKubernetesDiff(state map[string]map[string]any, documents []map[string]any, namespace string, output io.Writer) (int, error) {
	changed := false
	for _, object := range documents {
		key, _, err := developmentKubernetesKey(object, namespace)
		if err != nil {
			return 2, err
		}
		before, after := developmentKubernetesComparable(state[key]), developmentKubernetesComparable(object)
		if bytes.Equal(before, after) {
			continue
		}
		changed = true
		left, right := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
		fmt.Fprintf(output, "diff -u -N LIVE/%s MERGED/%s\n--- LIVE/%s\n+++ MERGED/%s\n@@ -1,%d +1,%d @@\n", key, key, key, key, len(left), len(right))
		for _, line := range left {
			fmt.Fprintln(output, "-"+line)
		}
		for _, line := range right {
			fmt.Fprintln(output, "+"+line)
		}
	}
	if changed {
		return 1, nil
	}
	return 0, nil
}

func applyDevelopmentKubernetesObject(state map[string]map[string]any, object map[string]any, namespace string) error {
	key, meta, err := developmentKubernetesKey(object, namespace)
	if err != nil {
		return err
	}
	version, uid := 0, fmt.Sprintf("development-%x", sha256.Sum256([]byte(key)))
	if previous := state[key]; previous != nil {
		previousMeta := previous["metadata"].(map[string]any)
		if meta["uid"] != previousMeta["uid"] || meta["resourceVersion"] != previousMeta["resourceVersion"] {
			return errors.New("development resource version conflict")
		}
		uid, _ = previousMeta["uid"].(string)
		version, _ = strconv.Atoi(fmt.Sprint(previousMeta["resourceVersion"]))
	}
	meta["uid"], meta["resourceVersion"] = uid, strconv.Itoa(version+1)
	state[key] = object
	return nil
}
