# AreaSong Ops 自动任务接入模板

自动任务是已有 cron/systemd 调度的只读治理投影。控制面展示调度来源、新鲜度和最近成功证据，但不接管调度；只有通过风险审查并写入适配器代码固定白名单的低风险任务，才可开放“立即补跑”。

## 安全边界

- cron/systemd 始终是调度配置和执行周期的唯一权威。
- 网页和 API 不接受 unit、脚本路径、命令、参数、环境变量、target 或 source。
- 适配器按对象名选择代码内固定的 cron 文件、采集器、指标文件、指标名和 flock。
- 补跑必须复用原调度的 flock，失败时保留原指标文件，成功时验证新文件身份、新鲜度和时间不倒退。
- 备份、Docker 清理、发布、GitHub Issue 同步、合规归档、凭据、网络、权限、数据库和外部写操作不得使用通用补跑入口。

## 最小声明

自动任务必须位于 schema 4 的 `automaticTasks` 中，并通过 `adapterRef` 引用顶层受信适配器注册表：

```json
{
  "name": "example-collector",
  "objectId": "automatic-task:example-collector",
  "metadata": {
    "type": "automatic_task",
    "environment": "production",
    "owner": "operations",
    "criticality": "important",
    "lifecycle": "proposed",
    "maturity": "inspect_only"
  },
  "displayName": "示例采集任务",
  "description": "汇总固定来源并原子发布只读指标。",
  "template": "automatic-task-v1",
  "adapterRef": "automatic-task-v1",
  "automaticTask": {
    "schedule": "每分钟",
    "scheduleSource": "cron",
    "freshnessSeconds": 180
  },
  "actions": {
    "inspect": {
      "name": "inspect",
      "displayName": "检查状态",
      "enabled": true,
      "risk": "read_only",
      "targetMode": "none",
      "steps": ["inspect"],
      "timeoutSeconds": 30,
      "impact": "仅读取固定调度、采集器和新鲜度证据。",
      "rollback": "没有变更，无需回滚。",
      "scope": "示例采集任务"
    }
  }
}
```

新任务默认只能采用 `lifecycle: proposed` 和 `maturity: inspect_only`，并只开放 `inspect`。不能仅靠声明添加补跑能力。

## 补跑准入

开放 `rerun` 前必须同时证明：

1. 任务只刷新可重建的观测或清单产物，不修改业务数据、版本、权限、网络或调度。
2. 固定采集器本身使用临时文件加原子 rename 发布，失败不会破坏上一份有效证据。
3. 补跑能复用原调度的互斥锁，并能区分“已在运行”和真实失败。
4. preflight 能保存旧产物时间与文件身份，verify 能证明发布了新证据且结果仍在新鲜度窗口内。
5. 对象名、cron 文件、采集器、指标文件、指标名和锁路径全部写入 root-owned 适配器白名单，不从声明或请求动态读取。
6. 适配器测试覆盖未知对象、未知阶段、target/source 输入、并发锁、失败保留旧文件和成功原子发布。

满足条件后，才能在固定适配器和 schema 4 声明中同时加入 `rerun`，并将成熟度提升到 `manual_approval`。首次生产补跑仍需单独批准，不能作为部署 smoke 自动执行。

## 接入顺序

1. 只读核对现有 cron/systemd、固定采集器、锁和成功证据，不修改调度。
2. 以 `inspect_only` 声明对象，验证 API、网页、新鲜度和异常展示。
3. 如需补跑，完成准入审查并在适配器代码中加入固定白名单和测试。
4. 运行 Go、适配器、Shell、前端和 source preflight 全量门禁。
5. 部署 Runner、适配器、schema 4 配置和 Web；先只读验收。
6. 另行批准一次补跑，验证计划、确认、锁、原子发布、任务事件和审计证据。
