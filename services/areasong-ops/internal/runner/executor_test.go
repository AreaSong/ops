package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestCommandExecutorRejectsTrailingJSONAndContractMismatch(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "trailing", output: `{"ok":true,"summary":"ok"}\n{"extra":true}\n`, want: "多余输出"},
		{name: "identity", output: `{"schemaVersion":2,"action":"restart","phase":"apply","ok":true,"summary":"ok"}\n`, want: "身份不匹配"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			script := filepath.Join(directory, "adapter.sh")
			body := "#!/usr/bin/env bash\nprintf '%b' '" + test.output + "'\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := (CommandExecutor{}).Execute(context.Background(), ExecuteInput{
				Service: model.ServiceDefinition{Name: "demo", Adapter: script},
				Action:  "update", Phase: "apply", OperationDir: directory,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want=%q", err, test.want)
			}
		})
	}
}
