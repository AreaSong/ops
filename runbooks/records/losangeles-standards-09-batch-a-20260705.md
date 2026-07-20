# LosAngeles standards/09 批次 A 低风险收敛记录

执行时间：20260704T205812Z UTC
服务器：LosAngeles
依据：runbooks/records/losangeles-standards-09-handbook-coverage-20260705.md

## 范围

本批次只执行不重启 Docker、不改 Redis、不改 fstab、不限制 SSH 来源的低风险收敛。

## 已执行

1. 收紧 `/etc/observability/alertmanager-smtp-password` 权限为 `600 root:root`。
2. 将旧 `/opt/services/account-vault/app/docker-compose.yml` 改名为 `docker-compose.legacy.yml`，并新增 `README.ops.md` 说明生产入口是 `/opt/services/account-vault/compose.yml`。
3. 将主机时区从 `Europe/London` 调整为 `UTC`。
4. 新增 `/etc/ssh/sshd_config.d/90-ops-hardening.conf`，关闭 `X11Forwarding`；已执行 `sshd -t` 并 reload SSH。
5. 对主要 HTTPS 入口执行响应头只读探测。

## 验证

- `sshd -t` 通过。
- `sshd -T` 显示 `x11forwarding no`。
- 当前 SSH 会话未断开。
- Nginx 响应头探测已执行，结果见临时日志：`/tmp/losangeles-09-batch-a-20260704T205812Z.log`。

## 未执行

- 未重启 Docker。
- 未修改 Redis。
- 未修改 fstab。
- 未限制 SSH 来源 IP。
- 未新增 Nginx 安全头；本批次仅做只读探测。

## 回滚

- SMTP 权限可恢复为原权限，但不建议放宽。
- account-vault legacy compose 可改回原文件名，但不建议继续使用旧 compose。
- 时区可用 `timedatectl set-timezone Europe/London` 回滚。
- SSH hardening 可删除 `/etc/ssh/sshd_config.d/90-ops-hardening.conf` 后 `sshd -t && systemctl reload ssh`。
