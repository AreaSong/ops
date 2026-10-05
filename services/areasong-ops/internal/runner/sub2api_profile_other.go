//go:build !linux

package runner

import "errors"

func sub2apiHostExecutionError() error {
	return errors.New("Sub2API 非 Linux 平台明确拒绝真实执行；仅允许显式注入合成测试后端")
}
