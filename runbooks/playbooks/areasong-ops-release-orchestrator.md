# AreaSong Ops 单一发布入口

`services/areasong-ops/deploy/release-orchestrator.sh` 是控制面 Web + Runner
发布的唯一入口。它不执行业务服务、数据库恢复、流量切换或 Kubernetes 操作。

## 发布前

1. 在 GHCR/GitHub Actions 生成 schema 2 manifest、Runner 归档、checksum 和 Sigstore bundle。
2. 把四个文件放入受控暂存目录；manifest 中的 revision 必须是 40 位小写 SHA，Web image 必须是
   `ghcr.io/areasong/areasong-ops-web:<revision>@sha256:<64>`。
3. 将 `/opt/ops` 同步到 manifest revision 并确认工作树干净；编排器不会隐式 `pull`、`checkout` 或修改 Git。
4. 在批准的变更窗口内，以 root 运行：

```bash
sudo /opt/ops/services/areasong-ops/deploy/release-orchestrator.sh deploy \
  --manifest /tmp/areasong-ops-release-<revision>.json \
  --runner-archive /tmp/areasong-ops-runner-<revision>-linux-amd64.tar.gz \
  --checksum /tmp/areasong-ops-runner-<revision>-linux-amd64.tar.gz.sha256 \
  --sigstore-bundle /tmp/areasong-ops-runner-<revision>-linux-amd64.tar.gz.sigstore.json
```

若只需创建可审计的部署计划而不改生产，使用相同参数把 `deploy` 换成 `plan`（生产 state 目录
为 root-only，因此同样使用 `sudo`）。计划输出的
`deploymentId` 是后续 `status`/`rollback` 的唯一键。

## 固定执行链

```text
参数/签名校验 → 源码与生产只读预检 → 唯一 deployment ID
→ Runner/Web/Compose/env/SQLite/image inspect 备份
→ Runner 解包、安装、daemon-reload、restart、socket/health/revision
→ 拉取并校验固定 Web digest、绑定本地 immutable tag、仅重建 Web
→ Web health/隔离检查 → runtime preflight → 审计收口
```

Runner 主程序固定安装到
`/usr/local/libexec/areasong-ops/runner/areasong-ops-runner`；Updater 固定安装到
`/usr/local/libexec/areasong-ops/areasong-ops-runner-updater`。Updater 自身不放入 `runner/`
目录，因为它需要在独立 systemd oneshot 中原子替换该目录内的 Runner。发布前备份、安装、
回滚和 installed preflight 必须使用这两个不同的固定路径。

每一步状态原子写入 `/var/lib/areasong-ops/release-orchestrator/deployments/<id>/state.json`，
审计追加到同目录 `audit.jsonl`。状态目录和备份材料均为 root-only；日志只记录摘要、路径名和
结果，不记录环境文件内容、Token、密码或命令输出。

## 幂等与回滚

- 相同 deployment ID 只能重放完全相同的 manifest 摘要；已成功的发布重试不会再次 restart
  Runner 或 recreate Web。已回滚/需要人工关注的 ID 必须新建计划。
- Runner 成功前不会触碰 Web。任一阶段失败立即停止后续阶段，按实际已改变组件逆序恢复；
  回滚失败会保留全部证据并将状态置为 `needs_attention`，不会删除备份。
- `rollback <deploymentId>` 只使用该部署生成的备份，不能指定任意源路径；恢复后仍需重新
  通过 health/preflight 才能人工关闭变更。

## 只读状态与清理

```bash
sudo /opt/ops/services/areasong-ops/deploy/release-orchestrator.sh status <deploymentId>
sudo -k
```

发布结束后保留 state、audit、SQLite 快照和文件备份，按备份保留策略统一清理；不得在失败后
手工删除恢复材料或用旧二进制写入数据库。
