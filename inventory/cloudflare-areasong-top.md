# areasong.top Cloudflare 与证书策略台账

更新时间：2026-07-03 18:55 BST

## 范围

- Zone：`areasong.top`
- 生产主机：`LosAngeles`
- 源站公网 IP：`23.185.200.12`
- 源站入口：Nginx `80/443`

本台账基于服务器侧 Nginx 配置、源站证书、公开 DNS、公开 HTTPS 响应头和源站 SNI 握手结果整理。未登录 Cloudflare 控制台，因此 WAF、安全规则、精确 DNS TTL、SSL/TLS 模式等控制台项标记为待确认。

## Zone 与权威 DNS

| 项目 | 当前值 | 核查方式 |
| --- | --- | --- |
| Zone | `areasong.top` | 公开 DNS |
| NS | `ace.ns.cloudflare.com`、`mary.ns.cloudflare.com` | `dig NS areasong.top` |
| 源站 IP | `23.185.200.12` | 本机 inventory / DNS-only 记录 |

## DNS 与代理状态

| 域名 | 用途 | DNS/代理状态 | 公开 HTTPS 信号 | 源站后端 |
| --- | --- | --- | --- | --- |
| `resume.areasong.top` | resume-jadeai | Cloudflare 代理，A/AAAA 返回 Cloudflare 边缘地址 | `server: cloudflare`、`cf-ray`，首页最终 200 | `127.0.0.1:2082` |
| `sorryiossearch.areasong.top` | account-vault-web | Cloudflare 代理，A/AAAA 返回 Cloudflare 边缘地址 | `server: cloudflare`、`cf-ray`，`/health` 200 | `127.0.0.1:8392` |
| `monitor.areasong.top` | Grafana | Cloudflare 代理，A/AAAA 返回 Cloudflare 边缘地址 | `server: cloudflare`、`cf-ray`，`/` 302 到 `/login` | `127.0.0.1:3000` |
| `cpa.areasong.top` | sub2api | DNS-only，A 返回 `23.185.200.12` | `server: nginx/1.24.0 (Ubuntu)`，`/health` 200 | `127.0.0.1:8080` |
| `log.areasong.top` | x-ui / xray 入口 | DNS-only，A 返回 `23.185.200.12` | `server: nginx/1.24.0 (Ubuntu)`，`/` 200 | `127.0.0.1:46585`、`127.0.0.1:10000`、`127.0.0.1:2096` |

说明：

- “Cloudflare 代理 / DNS-only”是根据公开 DNS 结果和 HTTP 响应头推断。
- Cloudflare 控制台中的橙云/灰云状态、TTL、备注、规则命中情况仍需控制台确认。
- `log.areasong.top` 的 x-ui 管理面板隐藏路径不进入台账；Nginx 配置核查时已脱敏。

## 源站证书策略

| 源站域名 | Nginx 证书 | 源站 SNI 证书 | 有效期 | 策略 |
| --- | --- | --- | --- | --- |
| `resume.areasong.top` | `/etc/ssl/cf/top/origin.pem` | Cloudflare Origin CA，SAN `*.areasong.top`、`areasong.top` | 2026-07-01 至 2041-06-27 | Cloudflare 代理域名使用 Origin Certificate |
| `sorryiossearch.areasong.top` | `/etc/ssl/cf/top/origin.pem` | Cloudflare Origin CA，SAN `*.areasong.top`、`areasong.top` | 2026-07-01 至 2041-06-27 | Cloudflare 代理域名使用 Origin Certificate |
| `monitor.areasong.top` | `/etc/ssl/cf/top/origin.pem` | Cloudflare Origin CA，SAN `*.areasong.top`、`areasong.top` | 2026-07-01 至 2041-06-27 | Cloudflare 代理域名使用 Origin Certificate |
| `cpa.areasong.top` | `/etc/letsencrypt/live/cpa.areasong.top/fullchain.pem` | Let's Encrypt，SAN `cpa.areasong.top` | 2026-07-02 至 2026-09-30 | DNS-only 直连域名使用公开可信证书 |
| `log.areasong.top` | `/etc/letsencrypt/live/log.areasong.top/fullchain.pem` | Let's Encrypt，SAN `log.areasong.top` | 2026-07-01 至 2026-09-29 | DNS-only 直连域名使用公开可信证书 |

私钥路径不写入台账明细；原则是 root-only 存放，不进入 Git。

## 公网证书表现

| 域名 | 公网证书 issuer | 公网证书 SAN | 说明 |
| --- | --- | --- | --- |
| `resume.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书 |
| `sorryiossearch.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书 |
| `monitor.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书 |
| `cpa.areasong.top` | Let's Encrypt `YE1` | `cpa.areasong.top` | 源站直连证书 |
| `log.areasong.top` | Let's Encrypt `YE2` | `log.areasong.top` | 源站直连证书 |

## 续期与维护

| 项目 | 当前状态 |
| --- | --- |
| Cloudflare Origin Certificate | 长期有效至 2041-06-27；仍需记录控制台创建人、用途和轮换计划 |
| Let's Encrypt / acme.sh | root crontab 存在每日 acme.sh cron：`13 23 * * *` |
| 监控 | Blackbox 已监控 HTTPS 可用性和证书临期；应用健康 Dashboard 已覆盖 `resume`、`sorryiossearch`、`cpa` |

## 控制台待确认项

这些项目无法仅从源站和公网响应完整确认，需要 Cloudflare 控制台或 Cloudflare API：

- DNS 记录完整清单、TTL、备注、是否橙云。
- SSL/TLS 模式是否为 Full (strict)。
- Always Use HTTPS、Automatic HTTPS Rewrites、Minimum TLS Version。
- WAF 自定义规则、托管规则、安全级别、Bot Fight Mode。
- Rate Limiting / DDoS / 防火墙事件。
- Cache Rules / Page Rules / Transform Rules。
- Origin Rules / Redirect Rules。
- Cloudflare Origin Certificate 的控制台记录、创建时间、过期提醒和轮换负责人。

## 当前建议

1. Cloudflare 代理域名继续使用 Cloudflare Origin Certificate；DNS-only 直连域名继续使用 Let's Encrypt。
2. 若未来将 `cpa.areasong.top` 或 `log.areasong.top` 改为 Cloudflare 代理，需要同步更新本台账、Nginx 证书策略和 Prometheus target。
3. 在 Cloudflare 控制台补齐 WAF、DNS TTL、SSL/TLS 模式后，把本文件中的“待确认项”更新为已确认配置。
