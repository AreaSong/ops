//go:build linux

package runner

import "errors"

func sub2apiHostExecutionError() error {
	return errors.New("Sub2API Linux 隔离、daemon、迁移及 BGSAVE 尚未验收；B1 不提供真实执行后端")
}
