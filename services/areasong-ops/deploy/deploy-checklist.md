# AreaSong Ops 部署检查清单

## 部署前

- [ ] 当前 `/opt/ops` commit 与批准 commit 一致且工作树无非预期变更。
- [ ] `127.0.0.1:3200` 未占用。
- [ ] `areasong-ops` 组已创建，并记录 GID 到 Compose env。
- [ ] Cloudflare Access Application AUD 已写入 `/etc/areasong-ops/web.env`。
- [ ] Grafana HTTPS origin 已写入 `OPS_GRAFANA_URL`，且不包含路径、查询或认证信息。
- [ ] 本机 `http://127.0.0.1:9093/-/ready` 返回成功，Runner 只连接 loopback Alertmanager v2 API。
- [ ] schema 3 服务声明的 `objectId` 稳定，告警 matcher 精确，维护静默白名单为阻断映射子集。
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
- [ ] `/v1/alerts` 只投影声明映射的活动阻断告警；Alertmanager 不可用时明确返回 `503`，其他只读页面仍可用。
- [ ] 在隔离验收中证明：活动阻断告警拒绝生产执行，映射外告警不阻断，静默 matcher 与最长到期时间符合声明。
- [ ] 在隔离验收中证明：任务失败提前解除静默；任务成功进入观察，收口前解除静默并复核被其他静默覆盖的活动告警。
- [ ] 删除已不存在的静默按幂等成功处理；遗留静默可在 Alertmanager 以计划注释和精确 matcher 定位。
- [ ] 不执行 update、rollback、restart、backup 或 restore-drill 作为首次部署 smoke。
- [ ] Prometheus 规则、中文告警、Grafana 自监控面板通过。
- [ ] inventory、端口、备份覆盖与 runbook 已更新并提交。
- [ ] `deploy/preflight.sh runtime` 证明 Web/Runner revision、Socket 权限和容器隔离一致。

## 回滚

- [ ] 恢复上一 Runner 二进制并重启单个 Runner unit。
- [ ] 将 Compose env 恢复为上一 Web commit tag并只重建 Web。
- [ ] 如 Nginx 新站点异常，恢复配置后 `nginx -t` 再 reload。
- [ ] 保留 `/var/lib/areasong-ops` 和审计证据，不恢复任何业务数据库。
