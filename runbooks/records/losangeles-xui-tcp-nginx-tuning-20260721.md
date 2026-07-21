# LosAngeles x-ui/xray 链路 TCP + Nginx 调优

日期：2026-07-21  
级别：L1  
主机：LosAngeles（23.185.200.12）

## 变更原因

只读体检发现：BBR/fq 已启用，面板本机 TTFB ~11ms，HTTP/2 + gzip 已生效；客户端经 443 平均 RTT ~127ms、重传约 2.2%。瓶颈在长肥管道默认 TCP 缓冲上限（`tcp_wmem` max 4MB）与空闲后慢启动，以及 Nginx `/as` WebSocket 反代默认 `proxy_read_timeout 60s` 会掐空闲隧道。

线路无法更换，仅做服务器侧可回滚调优。

## 变更内容

### 1. sysctl：`/etc/sysctl.d/98-ops-tcp-tuning.conf`

| 参数 | 变更前 | 变更后 |
|------|--------|--------|
| `net.core.rmem_max` / `wmem_max` | 212992 | 16777216 |
| `net.ipv4.tcp_rmem` max | 6291456 | 16777216 |
| `net.ipv4.tcp_wmem` max | 4194304 | 16777216 |
| `net.ipv4.tcp_slow_start_after_idle` | 1 | 0 |
| `net.ipv4.tcp_mtu_probing` | 0 | 1 |
| `vm.swappiness` | 60 | 10 |

`sysctl --system` 运行时生效，无需重启服务。

### 2. Nginx：`direct.log.areasong.top.conf` 的 `location /as`

- `proxy_read_timeout` / `proxy_send_timeout` → `300s`
- 不再 `include proxy-common.conf`（该 snippet 已含 60s 超时；同上下文再写会 `directive is duplicate`，`nginx -t` 失败）
- 在 location 内联 `proxy_http_version`、Upgrade/Connection 与转发头

`nginx -t` 通过后 `systemctl reload nginx`。

## 验证

- sysctl 读回与上表一致；BBR + fq 仍为当前值
- `nginx -t` successful；`nginx` / `x-ui` active
- `https://log.areasong.top/` → 200，TTFB ~10–13ms
- `https://cpa.areasong.top/health` → 200
- Prometheus `ALERTS{alertstate="firing"}` → none

## 回滚

```bash
# sysctl
sudo rm /etc/sysctl.d/98-ops-tcp-tuning.conf
sudo sysctl --system
# 或恢复备份：99-ops-baseline.conf.bak-20260721031103 / 99-bbr-x-ui.conf.bak-20260721031103

# nginx
sudo cp -a /etc/nginx/sites-available/direct.log.areasong.top.conf.bak-20260721031220 \
  /etc/nginx/sites-available/direct.log.areasong.top.conf
sudo nginx -t && sudo systemctl reload nginx
```

## 受控副本

- Ansible：`ansible/roles/common/tasks/main.yml` 增加 `98-ops-tcp-tuning.conf` 任务
- Nginx 站点文件当前不在 Git 受控副本中；运行态以 `/etc/nginx/sites-available/` 为准，备份见同目录 `*.bak-*`
