# LosAngeles standards09 A1 Nginx 安全响应头

日期：2026-07-05  
范围：Nginx 源站响应头、`server_tokens` 暴露收敛  
变更类型：低风险运行配置变更；仅 reload Nginx，不重启业务容器。

## 结论

状态：完成。

已完成：

- 全局启用 `server_tokens off`，源站不再暴露 `nginx/1.24.0 (Ubuntu)` 版本号。
- 新增 `/etc/nginx/snippets/security-headers-hsts.conf`。
- 新增 `/etc/nginx/snippets/security-headers-basic.conf`。
- 对 `resume.areasong.top`、`sorryiossearch.areasong.top`、`log.areasong.top` 补齐：
  - `Strict-Transport-Security`
  - `X-Content-Type-Options`
  - `X-Frame-Options`
  - `Referrer-Policy`
- 对 `cpa.areasong.top` 补 `Strict-Transport-Security`；其余安全头由应用侧已有 CSP / nosniff / frame / referrer 输出。
- 对 `monitor.areasong.top` 补 `Strict-Transport-Security` 和 `Referrer-Policy`；Grafana 已输出 nosniff / frame。

本次未配置全局 CSP。CSP 需要按应用逐站点设计，避免误伤前端脚本、Cloudflare Turnstile、Stripe、Grafana 等资源。

## 变更文件

系统配置：

- `/etc/nginx/nginx.conf`
- `/etc/nginx/snippets/security-headers-hsts.conf`
- `/etc/nginx/snippets/security-headers-basic.conf`
- `/etc/nginx/sites-available/cdn.resume.areasong.top.conf`
- `/etc/nginx/sites-available/cdn.sorryiossearch.areasong.top.conf`
- `/etc/nginx/sites-available/direct.cpa.areasong.top.conf`
- `/etc/nginx/sites-available/direct.log.areasong.top.conf`
- `/etc/nginx/sites-available/monitor.areasong.top.conf`

回滚备份：

- `/etc/nginx/ops-backups/20260705111139-security-headers`

## 验证

已执行：

- `nginx -t` 通过。
- `systemctl reload nginx` 成功。
- `systemctl is-active nginx` 返回 `active`。
- 源站本机 `curl -kIs --resolve <host>:443:127.0.0.1` 验证 5 个 HTTPS 入口。

源站验证结果：

- `resume.areasong.top`：`server: nginx`，HSTS / nosniff / frame / referrer 已出现。
- `sorryiossearch.areasong.top`：`server: nginx`，HSTS / nosniff / frame / referrer 已出现。
- `cpa.areasong.top`：`server: nginx`，HSTS 已出现；应用侧 CSP / nosniff / frame / referrer 保持存在。
- `log.areasong.top`：`server: nginx`，HSTS / nosniff / frame / referrer 已出现。
- `monitor.areasong.top`：`server: nginx`，HSTS / referrer 已出现；Grafana nosniff / frame 保持存在。

公网快速检查：

- `https://resume.areasong.top/` 返回 `307`。
- `https://sorryiossearch.areasong.top/` 返回 `200`。
- `https://cpa.areasong.top/health` 返回 `404`，与本次变更无关；公网首页仍正常。
- `https://log.areasong.top/` 返回 `200`。
- `https://monitor.areasong.top/api/health` 返回 `200`。

## 回滚

如需回滚：

1. 从 `/etc/nginx/ops-backups/20260705111139-security-headers` 恢复对应文件。
2. 执行 `nginx -t`。
3. 执行 `systemctl reload nginx`。
4. 重新跑源站 header 检查。
