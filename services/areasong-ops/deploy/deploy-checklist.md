# AreaSong Ops 部署检查清单

## 部署前

- [ ] 当前 `/opt/ops` commit 与批准 commit 一致且工作树无非预期变更。
- [ ] `127.0.0.1:3200` 未占用。
- [ ] `areasong-ops` 组已创建，并记录 GID 到 Compose env。
- [ ] Cloudflare Access Application AUD 已写入 `/etc/areasong-ops/web.env`。
- [ ] 当前 Runner、Compose、Nginx 和 Web image 身份已保存为回滚点。
- [ ] 最近完整备份 manifest 与 R2 校验均有效。

## 离线门禁

- [ ] `CGO_ENABLED=0 go test ./...`
- [ ] adapter Python tests、`bash -n`、`shellcheck` 通过。
- [ ] `npm run lint && npm run typecheck && npm run build` 通过。
- [ ] Runner export 与 Web Docker image 构建通过。
- [ ] `docker compose config --quiet` 通过。
- [ ] Nginx 配置在隔离前缀或生产 `nginx -t` 通过。
- [ ] `deploy/preflight.sh source` 通过；安装文件后 `installed` 模式通过。

## 上线验证

- [ ] `areasong-ops-runner.service` active，Socket 为 `0660 root:areasong-ops`。
- [ ] Web 容器为非 root、rootfs 只读、未挂载 Docker Socket。
- [ ] `http://127.0.0.1:3200/healthz` 返回 `200`。
- [ ] Web、Runner `/metrics` 可由本机 Prometheus 抓取。
- [ ] Cloudflare Access 未登录、错误邮箱、正确邮箱三条路径符合策略。
- [ ] AreaForge/Sub2API inspect 与 check 为只读，返回真实生产身份。
- [ ] 不执行 update、rollback、restart、backup 或 restore-drill 作为首次部署 smoke。
- [ ] Prometheus 规则、中文告警、Grafana 自监控面板通过。
- [ ] inventory、端口、备份覆盖与 runbook 已更新并提交。
- [ ] `deploy/preflight.sh runtime` 证明 Web/Runner revision、Socket 权限和容器隔离一致。

## 回滚

- [ ] 恢复上一 Runner 二进制并重启单个 Runner unit。
- [ ] 将 Compose env 恢复为上一 Web commit tag并只重建 Web。
- [ ] 如 Nginx 新站点异常，恢复配置后 `nginx -t` 再 reload。
- [ ] 保留 `/var/lib/areasong-ops` 和审计证据，不恢复任何业务数据库。
