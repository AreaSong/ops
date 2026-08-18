# Alertmanager 邮件投递与独立通知出口

## 目标与边界

恢复 Alertmanager 到 QQ SMTP 的中文邮件投递，同时保留 GitHub Issue 作为独立
critical 告警出口。`AlertmanagerNotificationFailures` 只由 GitHub watchdog 接管，
不得再次投递到已经失败的邮箱。

SMTP 授权码只允许在交互式隐藏提示中输入，不能进入 Git、聊天、shell 历史、
进程参数或日志。凭据文件固定为
`/etc/observability/alertmanager-smtp-password`，保持 root 所有且不可被组或其他用户写入。

## 诊断顺序

1. 查询 `AlertmanagerNotificationFailures` 和最近一小时
   `alertmanager_notifications_failed_total` 增量。
2. 检查 Alertmanager 日志中的 SMTP 状态码。`535 Login fail` 表示认证被 QQ 拒绝，
   不能用重启或放宽文件权限代替授权码轮换。
3. 检查 `alertmanager_github_issue_sync_success == 1` 和同步新鲜度，确认独立出口可用。
4. 检查 `alertmanager_runtime_input_stale`：`config=1` 需要校验并重载，
   `credential=1` 需要重建 Alertmanager 容器。

## 安全轮换

先执行 fresh backup，并确认回滚集包含 `/opt/ops/observability` 和
`/etc/observability`。在共享终端运行：

```bash
cd /opt/ops
sudo python3 scripts/deploy/rotate-alertmanager-smtp.py
```

工具通过隐藏提示读取新授权码，先执行 STARTTLS、认证和测试邮件投递；验证失败时
不会修改生产凭据。验证成功后创建 root-only 备份并原子替换凭据文件，输出的
`backup=` 路径是本次回滚点。

单文件 bind mount 绑定的是 inode。凭据原子替换后，运行中容器仍会看到旧 inode，
因此必须校验配置并仅重建 Alertmanager：

```bash
cd /opt/ops
scripts/tests/validate_observability_configs.sh
cd observability
sudo docker compose up -d --force-recreate --no-deps alertmanager
curl -fsS http://127.0.0.1:9093/-/ready
```

Alertmanager 配置或模板变更后同样执行上述校验和重建。Prometheus 规则变更则在
验证后调用其 lifecycle reload：

```bash
curl -fsS -X POST http://127.0.0.1:9090/-/reload
```

## 主机任务与调度器 policy

先以 check mode 预演，再部署运行输入采集器：

```bash
cd /opt/ops/ansible
ansible-playbook observability-host-jobs.yml --check --diff --limit LosAngeles
ansible-playbook observability-host-jobs.yml --limit LosAngeles
```

清退旧 AreaForge 自动 updater 前必须证明受控 agent timer 正常：

```bash
ansible-playbook areaforge-update-scheduler-policy.yml --check --diff --limit LosAngeles
ansible-playbook areaforge-update-scheduler-policy.yml --limit LosAngeles
```

policy 会保持 `areaforge-update-agent.timer` enabled/active，并停止、禁用、mask
`areaforge-updater.timer` 和 `areaforge-updater.service`，最后清除历史 failed 状态。
失败时恢复变更前捕获的旧单元状态。

## 灰度验收

1. 确认轮换工具的 STARTTLS 测试邮件已收到且中文正常。
2. 通过 Alertmanager API 注入一条短时、非 critical 的合成告警，验证实际模板和路由；
   不使用 critical，避免触发 GitHub Issue：

   ```bash
   starts_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   ends_at="$(date -u -d '+3 minutes' +%Y-%m-%dT%H:%M:%SZ)"
   curl -fsS -X POST http://127.0.0.1:9093/api/v2/alerts \
     -H 'Content-Type: application/json' \
     --data "[{\"labels\":{\"alertname\":\"Stage8EmailDeliveryProbe\",\"severity\":\"warning\",\"scope\":\"business\",\"service\":\"alertmanager\",\"owner\":\"areasong-ops\"},\"annotations\":{\"summary\":\"阶段 8 邮件投递验收\",\"description\":\"这是一条受控合成告警。\"},\"startsAt\":\"$starts_at\",\"endsAt\":\"$ends_at\"}]"
   ```

3. 确认通知成功增量大于零、失败增量停止增长，Alertmanager 日志无新 `535`。
4. 确认 `alertmanager_runtime_input_check_success == 1`，两个
   `alertmanager_runtime_input_stale` 均为 0。
5. 确认 `alertmanager_github_issue_sync_success == 1`；通知失败告警仍可被 GitHub
   watchdog 观察，但不会再次进入 email receiver。
6. 确认旧 AreaForge 单元 masked/inactive，新 agent timer enabled/active，
   `systemctl --failed` 不再包含旧 updater。
7. 重新生成不发送邮件的 Daily Ops Audit，确认 `systemd_failed=0` 和 critical 清零。

## 回滚

SMTP 或 Alertmanager 灰度失败时，使用轮换输出的备份路径恢复并重建容器：

```bash
cd /opt/ops
sudo python3 scripts/deploy/rotate-alertmanager-smtp.py \
  --restore-from /var/backups/ops/manual/alertmanager-smtp-rotation-<UTC>/alertmanager-smtp-password.before
cd observability
sudo docker compose up -d --force-recreate --no-deps alertmanager
```

配置回滚到变更前 Git 提交后再次校验并重建 Alertmanager、reload Prometheus。
host jobs 使用 `ansible/observability-host-jobs-rollback.yml` 切回上一完整 generation。
调度器 policy 自身失败会自动恢复捕获状态；仅在整批变更回滚且确实需要恢复旧调度时，
才显式 unmask 并重新 enable 旧 timer。
