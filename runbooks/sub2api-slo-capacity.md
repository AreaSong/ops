# sub2api SLO 与容量症状

## 初始目标

- 业务 journey synthetic availability：滚动 30 天 `99.9%`。
- 月度错误预算：约 43.2 分钟不可用。
- 快速燃烧：5 分钟和 1 小时窗口同时超过 `14.4x`。
- 持续燃烧：30 分钟和 6 小时窗口同时超过 `6x`。

同一 journey 包含多个探针时取最差值：任一步失败即视为该 journey 失败。
规则部署后需要积累自身的 1 分钟 recording series；Dashboard 的 coverage
达到 95% 前，不触发 30 天预算低或预算耗尽告警。
30 天 availability、coverage 和 budget 每小时重算，短窗口 burn rate 每分钟重算。
Prometheus 对刚缺失的原始探针样本有默认约 5 分钟 lookback 宽限；独立的
Blackbox target-down 告警负责更快识别采集失败，coverage 用于反映长期数据完整性。

## 指标边界

当前精确 SLO 仅使用 `probe_success{scope="business"}`。Nginx 日志产生的
`business_http_*_last_5m` 是每分钟重算的滑动窗口 gauge，不能使用 `rate()` 或
`increase()`，因此只作为运营症状，不作为精确请求错误预算。

正式 HTTP 成功率和时延 SLO 需要应用或受控旁车提供累计 counter/histogram：

```text
sub2api_http_requests_total{route,method,code_class,outcome}
sub2api_http_request_duration_seconds_bucket{route,method,le}
sub2api_inflight_requests{route}
sub2api_upstream_requests_total{provider,outcome}
sub2api_quota_rejections_total{scope}
sub2api_stream_sessions_total{outcome}
```

标签不得包含 token、用户、完整 URL、请求体或任意高基数标识。

## no available account

`write-sub2api-capacity-metrics.sh` 每分钟只读取最近五分钟 Docker 日志，输出：

- `sub2api_capacity_metrics_last_run_timestamp`
- `sub2api_log_check_success`
- `sub2api_no_available_account_events_last_5m`

出现事件说明应用账户池或上游账户可用性不足。先检查账户禁用、额度、冷却、上游
认证失败和并发占用，不应通过无限提高重试次数掩盖容量不足。

## 依赖容量检查

```promql
sum by (service, instance) (pg_stat_activity_count{service="sub2api"})
/
clamp_min(max by (service, instance) (pg_settings_max_connections{service="sub2api"}), 1)

redis_memory_used_bytes{service="sub2api"}
/
clamp_min(redis_memory_max_bytes{service="sub2api"}, 1)
```

连续 15 分钟同时出现业务延迟/错误变坏和数据库、Redis、CPU 任一饱和时，才进入
扩容或限流决策。不要只凭主机 CPU 调整连接池。`REDIS_POOL_SIZE=1024` 必须在隔离
压测后按峰值并发和 Redis/PID/FD 上限重新校准。

容器 collector 同时提供 `docker_container_cpu_limit_usage_ratio`、
`docker_container_memory_usage_ratio`、`docker_container_pids_usage_ratio`、restart、
health 和 OOM 状态。资源告警必须与业务 SLO/症状联合判断，不能单独作为扩容结论。

## 验证

```bash
bash observability/scripts/tests/test_sub2api_capacity_metrics.sh
promtool check rules observability/prometheus/rules/slo.yml
promtool test rules observability/prometheus/rules/tests/slo.test.yml
bash observability/prometheus/rules/tests/test_slo_rules.sh
```

生产上线后确认 cron 每分钟运行、三项 textfile 指标可查询、Dashboard 有数据，并用
受控测试日志验证 warning/critical 的触发与恢复。测试日志不得包含真实账号或凭据。
