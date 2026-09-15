# AreaSong Ops 单一发布入口

`services/areasong-ops/deploy/release-orchestrator.sh` 是控制面 Web + Runner
发布的唯一入口。它不执行业务服务、业务数据库恢复、流量切换或 Kubernetes 操作。
本入口可以在有完整隔离证据时恢复控制面自己的 SQLite；这不授权恢复任何业务数据库。

本文是执行合同，不是批准。生产部署、停机、备份和状态写入须纳入已确认的变更单元。

## 发布前

1. 在 GHCR/GitHub Actions 生成 schema 2 manifest、Runner 归档、checksum 和 Sigstore bundle。
2. 把四个文件放入受控暂存目录；manifest 中的 revision 必须是 40 位小写 SHA，Web image 必须是
   `ghcr.io/areasong/areasong-ops-web:<revision>@sha256:<64>`。
3. 将 `/opt/ops` 同步到 manifest revision 并确认工作树干净；编排器不会隐式 `pull`、`checkout` 或修改 Git。
4. 候选 Runner 必须支持 `releaseProtocol: 1`，并绑定正确的版本、revision 和 schema。
   入口在停止服务前检测 `--release-info`，不支持时拒绝部署，不回落到旧入口。
   不要以 root 裸跑不支持协议的旧二进制探测参数，它可能忽略参数而启动业务执行器。
5. 检查活动任务、观察期、批次、远程 assignment、未收口回执，以及独立 Updater unit 和
   systemd 排队作业。远程 Runner 的静止状态不能由本机空库证明，本入口不覆盖该部署模式。
6. 在批准的变更窗口内，以 root 运行：

```bash
sudo /opt/ops/services/areasong-ops/deploy/release-orchestrator.sh deploy \
  --manifest /tmp/areasong-ops-release-<revision>.json \
  --runner-archive /tmp/areasong-ops-runner-<revision>-linux-amd64.tar.gz \
  --checksum /tmp/areasong-ops-runner-<revision>-linux-amd64.tar.gz.sha256 \
  --sigstore-bundle /tmp/areasong-ops-runner-<revision>-linux-amd64.tar.gz.sigstore.json
```

`plan` 只验证制品并保存计划，不重启组件，也不代表生产预检已经通过。
它仍会向 root-only state 目录写入计划和审计，因此同样需要变更批准；不能把它当作纯只读 dry-run。
计划输出的 `deploymentId` 是后续 `status`/`rollback` 的唯一键。

## 固定执行链

```text
参数/签名/协议校验 → 源码、运行态和活动任务预检 → 预取固定 Web digest
→ 停 Web、停 Runner → 复验任务、独立 Updater、systemd 作业与 cgroup
→ 完整备份 → 持久化发布占用和维护标记
→ 安装 Runner → 维护模式迁移 SQLite、提供 Unix health/metrics
→ 仅重建 Web → maintenance preflight（业务 API 必须返回 503）
→ 持久化 activationStarted → 解除维护标记 → 正式启动 Runner
→ runtime preflight → 审计收口、释放本次占用
```

Runner 主程序固定安装到
`/usr/local/libexec/areasong-ops/runner/areasong-ops-runner`；Updater 固定安装到
`/usr/local/libexec/areasong-ops/areasong-ops-runner-updater`。Updater 自身不放入 `runner/`
目录，因为它需要在独立 systemd oneshot 中原子替换该目录内的 Runner。发布前备份、安装、
回滚和 installed preflight 必须使用这两个不同的固定路径。

每一步状态原子写入 `/var/lib/areasong-ops/release-orchestrator/deployments/<id>/state.json`，
审计追加到同目录 `audit.jsonl`。状态目录和备份材料均为 root-only；日志只记录摘要、路径名和
结果，不记录环境文件内容、Token、密码或命令输出。

备份还包括服务目录配置、受控适配器、Web env、Compose、运行环境、脱敏 image inspect 和
自包含 SQLite 快照，保存 SHA-256、schema、UID/GID/mode，不改变既有父目录权限。
维护标记绑定 deployment、revision、程序/配置摘要及 boot ID；维护模式不构造业务 Engine，
不恢复任务、不运行清理循环，也不开放业务 API。正式运行态验收不能接受维护模式冒充正常。

## 幂等与回滚

- 相同 deployment ID 只能重放完全相同的 manifest 摘要；已成功的发布重试不会再次 restart
  Runner 或 recreate Web，只可收尾本次已完成的私有占用清理。
- 部署与手动回退共用全局锁，锁内重读状态；已有未收口发布不能被新 ID 绕过。
  旧状态记录没有新版隔离证据时只可查询，不能直接用于自动回退。
- 正式激活前失败，仅当同一次启动、维护标记、当前程序/unit、完整备份和静止状态都可证明时
  才允许完整回退。先停尽连接并保留失败数据库及 WAL/SHM，再恢复并验证原 schema/配置，
  最后切换旧程序；切换旧入口前先持久化回退激活边界。
- 已越过正式激活或回退激活边界、发生重启、隔离失效或材料漂移时，不用旧快照覆盖后续
  审批、审计或业务状态。分别尝试停住 Web/Runner，保留证据并进入 `needs_attention`。
  停机失败不自动重发同一写命令；隔离失效必须在开始停机前落盘，防止中断丢失拒绝恢复的证据。
- `rollback <deploymentId>` 只使用本次快照，不能指定任意来源；它不是已成功上线版本的通用
  降级入口。正式开放后的回退或 `needs_attention` 收口需要新的人工核对与变更批准，不能仅删标记放行。
- 回退后必须重新通过旧 Runner/Web health 和 installed/runtime preflight；任一验证失败
  继续保留现场，不宣称回退完成，也不恢复业务数据库。

## 发布与 CI 验收

- 普通非 root Go 测试之外，在无网络、无 Docker Socket、只读源码挂载的 Linux root 容器中
  执行真实 Runner 维护测试，关键用例不得被跳过。
- 签名前从实际 Runner 制品读取 `--release-info`，核对协议、版本、revision 和 schema；
  下载后的 SHA-256 与 Runner/Web 签名仍须按消费端流程复验。
- 本地和 CI 验收不能替代生产窗口内的运行态、双账号及功能启用验收。

## 只读状态与清理

```bash
sudo /opt/ops/services/areasong-ops/deploy/release-orchestrator.sh status <deploymentId>
sudo -k
```

发布结束后保留 state、audit、SQLite 快照和文件备份，按备份保留策略统一清理；不得在失败后
手工删除恢复材料或用旧二进制写入数据库。
