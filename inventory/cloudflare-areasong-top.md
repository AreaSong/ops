# areasong.top Cloudflare 与证书策略台账

更新时间：2026-07-04 08:14 BST

## 范围

- Zone：`areasong.top`
- 生产主机：`LosAngeles`
- 源站公网 IP：`23.185.200.12`
- 源站入口：Nginx `80/443`

本台账基于服务器侧 Nginx 配置、源站证书、公开 DNS、公开 HTTPS 响应头、源站 SNI 握手结果和 Cloudflare 控制台只读核对结果整理。控制台核对期间未修改 Cloudflare、DNS、Nginx 或服务配置。

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
| `www.areasong.top` | Cloudflare Access 保护的 Tunnel 应用入口 | Cloudflare Tunnel，目标 `hWin`，已代理 | `server: cloudflare`；未登录访问 302 到 `areasong.cloudflareaccess.com` 登录页 | Cloudflare Tunnel `hWin` |

说明：

- Cloudflare 代理 / DNS-only、TTL、Tunnel 记录基于 Cloudflare 控制台核对；公开 DNS 和 HTTP 响应头用于交叉验证。
- `log.areasong.top` 的 x-ui 管理面板隐藏路径不进入台账；Nginx 配置核查时已脱敏。
- `www.areasong.top` 当前作为 Cloudflare Access 保护的 Tunnel 应用入口保留；负责人为 `as`。

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
| `www.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书；Cloudflare Access 保护入口 |
| `cpa.areasong.top` | Let's Encrypt `YE1` | `cpa.areasong.top` | 源站直连证书 |
| `log.areasong.top` | Let's Encrypt `YE2` | `log.areasong.top` | 源站直连证书 |

## 续期与维护

| 项目 | 当前状态 |
| --- | --- |
| Cloudflare Origin Certificate | 控制台创建人和轮换负责人均为 `as`；用途为 `resume.areasong.top`、`sorryiossearch.areasong.top`、`monitor.areasong.top` 的 Cloudflare 代理源站证书；长期有效至 2041-06-27；过期前 180/90/30 天提醒 |
| Let's Encrypt / acme.sh | root crontab 存在每日 acme.sh cron：`13 23 * * *` |
| 监控 | Blackbox 已监控 HTTPS 可用性和证书临期；应用健康 Dashboard 已覆盖 `resume`、`sorryiossearch`、`cpa` |

## 治理元数据

| 对象 | 用途 | 创建人 | 负责人 | 提醒 / 处置 |
| --- | --- | --- | --- | --- |
| Cloudflare Origin Certificate `*.areasong.top` / `areasong.top` | `resume.areasong.top`、`sorryiossearch.areasong.top`、`monitor.areasong.top` 的代理回源 TLS 证书 | `as` | `as` | 2041-06-27 到期；过期前 180/90/30 天提醒并评估轮换 |
| Cloudflare Tunnel `hWin` / `www.areasong.top` | Cloudflare Access 保护的 Tunnel 应用入口 | `as` | `as` | 当前保留；若未来下线，需要先确认 Access 应用、Tunnel connector 和 DNS 记录依赖 |

## Cloudflare 控制台核对

2026-07-04 通过 Cloudflare 控制台只读核对以下配置：

| 类别 | 当前状态 | 说明 |
| --- | --- | --- |
| DNS TTL | 全部为自动 | `resume`、`sorryiossearch`、`monitor` 已代理；`cpa`、`log` 为 DNS-only；`www` 为 Cloudflare Tunnel `hWin` 且当前保留。 |
| SSL/TLS 模式 | Full (strict) | Cloudflare 会校验源站证书；与代理域名使用 Cloudflare Origin Certificate 的策略匹配。 |
| Universal SSL | 有效 | `*.areasong.top`、`areasong.top` 通用证书有效期至 2026-09-19；备份证书已签发。 |
| Always Use HTTPS | 已开启 | HTTP 请求由 Cloudflare 侧重定向到 HTTPS。 |
| Automatic HTTPS Rewrites | 已开启 | 用于修正可安全替换的混合内容链接。 |
| Minimum TLS Version | TLS 1.3 | 兼容性最严格；目前与已代理入口使用场景匹配。 |
| TLS 1.3 | 已开启 | 低于 TLS 1.3 的客户端无法访问已代理 HTTPS 入口。 |
| HSTS | 未开启 | 暂不启用，避免 includeSubDomains 等策略误伤多业务子域。 |
| WAF 自定义规则 | 未创建 | Free 计划当前 `0/5`。 |
| 速率限制规则 | 未创建 | Free 计划当前 `0/1`。 |
| Cloudflare 托管规则集 | 始终启用 | 控制台显示托管规则集启用。 |
| DDoS 防护 | 活动 | 网络层、SSL/TLS、HTTP DDoS 基础保护启用；未创建 DDoS 替代规则。 |
| 安全级别 | 自动化 / 始终受保护 | `I'm under attack` 模式禁用。 |
| AI 爬虫控制 | 已在所有页面阻止 | 控制台显示 AI crawler 阻止配置。 |
| Bot Fight Mode | 关闭 | JS 检测开启；未启用 Bot Fight，避免误伤业务流量。 |
| 浏览器完整性检查 | 已开启 | Cloudflare 侧基础请求检查已启用。 |
| 泄露凭据检测 | 未开启 | 可选功能，当前不启用。 |
| 热链接保护 | 未开启 | 当前无图片盗链专项需求。 |
| Page Rules | 未创建 | `0/3`。 |
| Redirect Rules | 未创建 | 无已创建重定向规则。 |
| Cache Rules | 未创建 | 无缓存规则和缓存响应规则。 |
| Origin Rules | 未创建 | 未配置回源地址、端口或 Host header 改写。 |
| Transform Rules | 未创建 | 未配置 URL、请求头或响应头转换。 |
| Workers 路由 | 未创建 | 未绑定 `areasong.top` 或子域 Workers 路由。 |

## 仍需补充项

- Cloudflare Origin Certificate 的实际提醒落地渠道仍需确认，例如日历、任务系统或后续自动化监控。
- `www.areasong.top` / Cloudflare Tunnel `hWin` 的后端应用细节仍需在 Cloudflare Zero Trust / Tunnel 控制台中补充。
- 若未来启用 HSTS、Bot Fight Mode、速率限制、WAF 自定义规则或缓存规则，需要先评估对 `log`、`cpa`、`monitor` 等入口的影响。

## 当前建议

1. Cloudflare 代理域名继续使用 Cloudflare Origin Certificate；DNS-only 直连域名继续使用 Let's Encrypt。
2. 若未来将 `cpa.areasong.top` 或 `log.areasong.top` 改为 Cloudflare 代理，需要同步更新本台账、Nginx 证书策略和 Prometheus target。
3. 继续保持 HSTS、Bot Fight Mode、热链接保护和自定义缓存规则关闭，除非后续有明确业务需求和回滚方案。
