# LosAngeles x-ui 流量审计

## 目标

对 x-ui/xray 节点做流量审计：完整记录"谁（客户端 email）在何时访问了哪个目的地（域名/IP）"，并按服务分类统计上下行字节用量。数据汇入既有 Loki + Prometheus + Grafana 栈。

## 能力边界

- 能看到：目的地域名/IP + 端口、发起用户（inbound client email）、时间、出站分类；按服务分类的累计上下行字节。
- 看不到：加密内容。节点只做转发，客户端与目标站之间是端到端 TLS，审计只涉及元数据（去了哪里），不含报文内容。
- 域名可见性依赖 inbound 开启 `sniffing`；直连 IP、部分 QUIC 连接可能仅显示 IP。

## 隐私边界

- 当前为单客户端自有节点（活跃 inbound 10000，client `eui4zb6d`）。
- access log 含目的地与 email，属敏感元数据；日志权限 `0644 root adm`，logrotate 保留 30 天压缩。
- 若将来节点接入多用户，需先告知使用者并评估合规。

## 数据链路

```
xray access log (/var/log/xray/access.log)
  -> Promtail(job=xray_access) -> Loki -> Grafana「流量审计」日志/频次面板

xray 路由按 geosite 分桶到 out-* 出站标签
  -> xray api statsquery(127.0.0.1:62789)
  -> write-xray-traffic-metrics.sh (cron, 每分钟)
  -> node_exporter textfile /var/lib/node_exporter/textfile_collector/xray-traffic.prom
  -> Prometheus(job=node) -> Grafana「流量审计」字节面板
```

## Xray 侧配置（3x-ui 面板，持久化于 x-ui.db）

`bin/config.json` 由面板生成，手改会被覆盖。以下改动通过面板「Xray 配置」模板 + inbound 编辑完成：

1. `log.access` = `/var/log/xray/access.log`（loglevel 保持 warning）
2. inbound 10000 `sniffing.enabled` = true（destOverride 保留 http/tls/quic）
3. `outbounds` 追加各分类 freedom 出站标签（out-google/out-youtube/out-netflix/out-x/out-meta/out-tiktok/out-telegram/out-openai/out-github/out-apple/out-microsoft）
4. `routing.rules` 追加各 `geosite:<类别> -> outboundTag`，未匹配落到 `direct`（看板显示为"其他（未分类）"）
5. `policy.system.statsOutboundUplink` / `statsOutboundDownlink` = true

分桶维护：新增/调整分类即在面板模板加一个出站标签 + 一条路由规则，看板 legend 用 `{{tag}}` 自动展示；无需改采集器。

## 变更前备份与回滚

```bash
sudo cp /usr/local/x-ui/bin/config.json /usr/local/x-ui/bin/config.json.bak-$(date -u +%Y%m%d%H%M%S)
sudo cp /etc/x-ui/x-ui.db /etc/x-ui/x-ui.db.bak-$(date -u +%Y%m%d%H%M%S)
```

回滚：面板还原模板并保存（自动重启 xray），或还原上面备份后 `sudo systemctl restart x-ui`。

## 正常输出

| 输出 | 位置 |
|---|---|
| 访问日志 | `/var/log/xray/access.log` |
| 采集任务日志 | `/var/log/observability/xray-traffic-metrics.log` |
| Prometheus 指标 | `/var/lib/node_exporter/textfile_collector/xray-traffic.prom` |
| Grafana | `流量审计`（uid `losangeles-xray-traffic-audit`） |

## 看板用法

- 顶部 `出站分类`（`$outbound`）、`用户`（`$email`）下拉可筛，默认 All；字节速率、本时段流量、Top 域名、连接速率面板都跟随。
- 「Top 目的地域名（按连接次数）」表用 LogQL 在查询期从 access log 抽取域名并 `topk(20)`，跟随时间选择器；这是"具体去了哪些域名"的权威视图。
- 「本时段流量（按分类与方向）」用 `increase(...[$__range])`，跟随时间范围且正确处理 xray 重启导致的计数器清零（不是自进程启动以来的累计）。
- 时间线上的红色注解来自 `ALERTS{service="x-ui"}`，标注 xray 审计告警何时触发。

## 手工核验

```bash
# 出站分类字节计数（需已开启出站统计与分桶路由）
/usr/local/x-ui/bin/xray-linux-amd64 api statsquery --server=127.0.0.1:62789 | grep 'outbound>>>out-'

# 采集器产物
sudo cat /var/lib/node_exporter/textfile_collector/xray-traffic.prom

# access log 是否在写、被 Loki 收录
sudo tail -n 5 /var/log/xray/access.log
```

Prometheus 中确认：`xray_outbound_traffic_bytes_total`、`xray_traffic_metrics_check_success == 1`。

## 相关告警

- `XrayTrafficMetricsStale`：采集器超过 5 分钟未成功（warning）。
- `XrayTrafficStatsQueryFailed`：`xray api statsquery` 读取失败（warning）。
