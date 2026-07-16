# GitHub 外部可用性监控

## 目标

由 GitHub Actions 从 LosAngeles 主机之外计划每 5 分钟并发检查六个公开 HTTPS 入口。任一入口出现 DNS、连接、TLS 或 HTTP 4xx/5xx 错误时，工作流失败并创建或更新带 `external-uptime` 标签和机器标记的 Issue；全部恢复后只关闭该工作流自己创建的未关闭 Issue。

## 边界

- 不使用服务器 SSH、R2、数据库或 SMTP 凭据。
- 只访问公开 HTTPS 根路径，不执行登录、写入或业务任务。
- account-vault 与 sub2api 使用公开 `/health`，其余服务检查公开入口。
- GitHub 调度可能延迟，不能替代秒级或电话级值守平台。
- 定时工作流只在默认分支生效；仓库长期无活动时应确认 GitHub 没有自动停用 schedule。
- 非默认分支和未勾选 `manage_issues` 的手工运行只做检查，不会创建、更新或关闭 Issue。

## 手工验证

1. 在默认分支的 Actions 中运行 `LosAngeles External Uptime`，先保持 `manage_issues=false`、`simulation_mode=none`。
2. 确认六个目标均显示 `OK`，TLS 校验未被绕过。
3. 经确认后在默认分支选择 `manage_issues=true`、`simulation_mode=failure` 手工运行，确认只创建一条带 `external-uptime-test` 标签和独立隐藏标记的测试 Issue，不改动生产监控 Issue。
4. 以同样输入再次运行，确认同一 Issue 正文被更新而不是创建重复项。
5. 保持 `manage_issues=true`、改为 `simulation_mode=recovery` 再次运行，确认该测试 Issue 自动关闭。
6. 确认人工创建或仅人工添加 `external-uptime` 标签的 Issue 未被修改。

本地回归验证：

```bash
bash scripts/monitor/tests/test_external_uptime_check.sh
bash scripts/monitor/tests/test_external_uptime_incident.sh
```

## 告警处理

收到 Issue 通知后，先从其他网络访问对应入口，再按 `host-unreachable.md`、`service-5xx.md` 或证书相关面板定位。若 GitHub 自身故障，以本机监控和人工检查作为临时补偿。
