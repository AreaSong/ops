# GitHub 外部可用性监控

## 目标

由 GitHub Actions 从 LosAngeles 主机之外每 5 分钟启动一轮监控；每轮内部按约 1 分钟间隔执行 5 次并发检查六个 HTTPS 入口。每个目标连续 3 次失败后才创建或更新故障 Issue，恢复后只关闭该工作流自己创建的未关闭 Issue。GitHub Actions 的 schedule 可能漂移，因此该链路不是秒级值守替代品。

## 边界

- 不使用服务器 SSH、R2、数据库或 SMTP 凭据。
- `monitor.areasong.top` 由 Cloudflare Access 保护后，工作流只使用专用 service token 的 `CF-Access-Client-Id` 和 `CF-Access-Client-Secret` 请求头；其他目标仍按公开 HTTPS 探测。
- GitHub Actions secrets 名称为 `CF_ACCESS_CLIENT_ID`、`CF_ACCESS_CLIENT_SECRET`。令牌不得写入仓库、日志或 Issue。
- 只访问只读健康路径，不执行交互登录、写入或业务任务。
- account-vault 与 sub2api 使用公开 `/health`，AreaForge 使用公开 `/api/health`；其余服务检查公开入口。Grafana 只使用专用 Cloudflare Access service token。
- 探针使用 GET、固定响应内容、`Cache-Control: no-cache, no-store` 与 `Pragma: no-cache` 请求头；不得使用 HEAD，也不得跟随用户登录流程。
- GitHub 调度可能延迟，不能替代秒级或电话级值守平台。
- 定时工作流只在默认分支生效；仓库长期无活动时应确认 GitHub 没有自动停用 schedule。
- LosAngeles 每 5 分钟通过现有 root-only GitHub Issues 凭据更新一个关闭状态的 heartbeat Issue；外部工作流读取其 UTC 时间戳，超过 600 秒视为 dead-man 失联，并使用独立 `external-heartbeat` Issue 通知。
- heartbeat Issue 正文只有固定标记、时间戳和用途说明；不得写入日志、Token、Access secret 或业务数据。发现重复 heartbeat Issue 时，外部检查必须失败并等待人工去重。
- 非默认分支和未勾选 `manage_issues` 的手工运行只做检查，不会创建、更新或关闭 Issue。
- 每月 1 日 03:17 UTC 运行独立 concurrency group 的 failure/recovery Issue 生命周期演练，不会被下一次五分钟探针取消。

## Cloudflare Access 当前配置

1. Self-hosted Application 为 `Grafana - monitor.areasong.top`，Application ID `8f78fba9-dadd-4ab0-ab18-41e895e7a32f`。
2. 人员策略仅允许 `song80184@gmail.com` 使用 OTP，会话时长 6 小时。
3. 自动化策略仅允许 service token `github-actions-grafana-health`，Token ID `f9008337-dab4-46ec-8802-c17ea2739634`。
4. token 于 2027-07-29 到期，负责人应在 2027-06-29 前创建替代 token、更新两个 GitHub secrets、验证后再撤销旧 token。
5. client ID/secret 只保存在 `AreaSong/ops` 的 `CF_ACCESS_CLIENT_ID`、`CF_ACCESS_CLIENT_SECRET` Actions secrets；不得复用浏览器 OTP 身份或输出 secret。
6. 未带 token 的请求应进入 Access 登录；带 token 的 `/api/health` 探针应返回成功。

## 手工验证

1. 在默认分支的 Actions 中运行 `LosAngeles External Uptime`，先保持 `manage_issues=false`、`simulation_mode=none`。
2. 确认六个目标均显示 `OK`，TLS 校验未被绕过。
3. 经确认后在默认分支选择 `manage_issues=true`、`simulation_mode=failure` 手工运行，确认只创建一条带 `external-uptime-test` 标签和独立隐藏标记的测试 Issue，不改动生产监控 Issue。
4. 以同样输入再次运行，确认同一 Issue 正文被更新而不是创建重复项。
5. 保持 `manage_issues=true`、改为 `simulation_mode=recovery` 再次运行，确认该测试 Issue 自动关闭。
6. 确认人工创建或仅人工添加 `external-uptime` 标签的 Issue 未被修改。
7. 确认 workflow 日志不显示 service token，月度任务使用 `monthly-simulation` concurrency group。
8. 确认正常调度的一轮产生约 5 个 round 结果；单次或两次抖动只记录为 `pending`，不创建生产 Issue；第三次连续失败才创建，恢复后关闭。
9. 确认 heartbeat Issue 的时间戳持续更新；受控停止 heartbeat 后，外部 workflow 在宽限期后创建独立 `external-heartbeat` Issue，恢复后关闭。

本地回归验证：

```bash
bash scripts/monitor/tests/test_external_uptime_check.sh
bash scripts/monitor/tests/test_external_uptime_incident.sh
bash scripts/monitor/tests/test_external_heartbeat_check.sh
bash scripts/monitor/tests/test_external_monitor_loop.sh
python3 -m unittest observability.scripts.tests.test_github_external_heartbeat
```

## 2026-07-29 基础链路生产验收记录

- 浏览器人员路径已完成 OTP，进入 Grafana 成功。
- 工作流 #191 正常模式成功：6/6 HTTPS 入口返回 200，Grafana 使用 service token 通过 Access；此前生产故障 Issue #5 自动关闭。
- 工作流 #192 受控故障模拟按预期失败并创建测试 Issue #86。
- 工作流 #193 受控恢复模拟成功并自动关闭测试 Issue #86。
- 验收日志和 Issue 未输出 Access client secret；测试 Issue 使用独立标记，不影响生产故障 Issue。

## 阶段 7 增强实施状态

- 本地实现已补齐固定响应内容校验、AreaForge JSON 健康路径、连续失败阈值、分钟级 round loop 和 heartbeat/dead-man 客户端。
- 生产部署前必须先将 heartbeat host cron 部署并确认 heartbeat Issue 已产生，再发布默认分支工作流；否则外部监控会把“尚未接入”误报为失联。
- 生产关闭门禁：正常探测、单次/两次抖动、三次连续失败、恢复、heartbeat 失联、heartbeat 恢复、重复 heartbeat Issue 和回滚证据均通过后，才将本节标记为完成。

## 告警处理

收到 Issue 通知后，先从其他网络访问对应入口，再按 `host-unreachable.md`、`service-5xx.md` 或证书相关面板定位。若 GitHub 自身故障，以本机监控和人工检查作为临时补偿。
