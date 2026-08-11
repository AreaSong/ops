# AreaSong Ops 通用服务模板

`compose-service-v1` 用一份声明复用控制面的网页、确认短语、任务阶段、服务锁、审计、备份、重启、发布发现、准备门禁、更新和回滚。模板适合“一个应用容器加若干依赖容器”的 Compose 服务。

## 接入边界

通用适配器负责：

- 比对 Git 管理的 Compose 与运行副本。
- 调用声明的分类备份作业。
- 只重建 `applicationService`，并验证依赖容器身份未变化。
- 只访问 `127.0.0.1` 或 `localhost` 的 HTTP health 地址。
- 查询 GitHub 最新发布，并读取静态或动态 prepared 记录。
- 把服务特有的恢复演练、发布准备、更新和回滚委托给固定 hook。

模板不提供任意 Shell、Compose 编辑、生产数据库恢复或自动更新。涉及迁移、认证 smoke、数据一致性和旧版本兼容时，必须提供服务特有 hook。

## 最小声明

```json
{
  "name": "demo",
  "displayName": "Demo",
  "description": "Demo Web 与 PostgreSQL",
  "template": "compose-service-v1",
  "adapter": "/usr/local/libexec/areasong-ops/adapters/compose-service.sh",
  "runtime": {
    "controlledCompose": "/opt/ops/services/demo/compose.yml",
    "runtimeCompose": "/opt/services/demo/compose.yml",
    "envFile": "/opt/services/demo/.env",
    "applicationService": "demo",
    "applicationContainer": "demo",
    "dependencyContainers": ["demo-postgres"],
    "healthUrl": "http://127.0.0.1:8090/health",
    "releaseRepository": "owner/demo",
    "releaseCatalog": "/opt/ops/services/demo/releases.json",
    "preparedReleaseDir": "/var/lib/areasong-ops/prepared-releases/demo",
    "inspectExecutable": "/usr/local/libexec/areasong-ops/hooks/demo-inspect.sh",
    "backupExecutables": ["/opt/ops/scripts/backup/backup-demo.sh"],
    "prepareExecutable": "/usr/local/libexec/areasong-ops/hooks/demo-prepare.sh",
    "updateExecutable": "/usr/local/libexec/areasong-ops/hooks/demo-update.sh"
  }
}
```

动作仍在同一服务对象的 `actions` 中声明。更新动作使用 `targetMode: signed_release_tag` 和 `readinessGate: prepared_release`；准备动作使用相同目标格式并在成功后发布动态 prepared 记录。

## Hook 契约

Runner 以以下参数调用所有适配器和 hook：

```text
<action> <phase> <operation-dir> <target> <source-dir>
```

成功时 stdout 必须只输出一个 JSON 对象：

```json
{"schemaVersion":2,"action":"update","phase":"health","ok":true,"summary":"阶段完成","data":{}}
```

Runner 会拒绝动作/阶段身份不匹配、尾随第二个 JSON 或其他多余输出。错误说明写到 stderr，并以非零状态退出。hook 必须是 root 拥有的普通文件、不可由组或其他用户写入，并设置 owner execute。服务声明中的所有路径必须为绝对路径。

变更动作应为每个阶段声明 `phaseSemantics`，明确 `effect`、`failurePolicy`、恢复点产消关系和回滚阶段。产生恢复点的备份阶段还需在顶层返回 `recoveryPoint`：它必须绑定当前 service/task，列出受控备份目录中的服务必需产物、大小和 SHA-256。Runner 完成二次验证并持久化后，`requiresRecoveryPoint` 阶段才会放行。

发布准备成功后，以原子方式写入：

```text
/var/lib/areasong-ops/prepared-releases/<service>/<tag>.json
```

记录至少包含 `tag`、`status: prepared`、当前身份、目标镜像摘要、迁移数量和隔离演练证据。更新 hook 必须再次验证该记录和运行时 expected-before，不能只依赖网页按钮状态。

## 接入顺序

1. 固定受控 Compose、运行 Compose、env、容器名、health 和发布仓库。
2. 实现只读 inspect hook；有数据服务时实现分类备份和隔离恢复 hook。
3. 在隔离网络中验证 fresh backup、目标迁移、目标 health、认证 smoke 和旧镜像兼容；不得发布宿主端口。
4. 将声明加入 `services.json`，执行测试、构建和 `preflight.sh installed`。
5. 部署后先运行 inspect/check/prepare，核对 prepared 记录和证据摘要。
6. 只有 prepared 门禁通过后才开放目标更新；生产升级仍需单独确认。
