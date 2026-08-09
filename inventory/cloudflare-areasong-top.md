# areasong.top Cloudflare 与证书策略台账

更新时间：2026-08-09 UTC

## 范围

- Zone：`areasong.top`
- 生产主机：`LosAngeles`
- 源站公网 IP：`23.185.200.12`
- 源站入口：Nginx `80/443`

本台账基于服务器侧 Nginx 配置、源站证书、公开 DNS、公开 HTTPS 响应头、源站 SNI 握手结果和 Cloudflare 控制面核对结果整理。2026-07-29 已按批准为 Grafana 创建 Access Application、人员与自动化策略及 service token；token secret 不进入本台账或 Git。`ops.areasong.top` 仅为仓库目标态，DNS、Nginx 与 Access 均尚未创建或验收，以下对应条目明确标为计划状态。

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
| `monitor.areasong.top` | Grafana | Cloudflare 代理并受 Access 保护，A/AAAA 返回 Cloudflare 边缘地址 | 未认证请求进入 Access 登录；允许邮箱完成 OTP 后进入 Grafana；专用 service token 的 `/api/health` 返回 200 | `127.0.0.1:3000` |
| `ops.areasong.top` | AreaSong Ops 控制面 | **计划/待验收**：目标为 Cloudflare 代理并受独立 Access Application 保护；当前不作为已生效 DNS 记录 | **待验收**：未认证请求应进入 Access 登录，仅允许 `song80184@gmail.com` OTP | 计划 `127.0.0.1:3200` |
| `forge.areasong.top` | AreaForge | Cloudflare 代理，A/AAAA 返回 Cloudflare 边缘地址 | `server: cloudflare`，公网入口可达 | `127.0.0.1:3020` |
| `cpa.areasong.top` | sub2api | DNS-only，A 返回 `23.185.200.12` | `server: nginx/1.24.0 (Ubuntu)`，`/health` 200 | `127.0.0.1:8080` |
| `log.areasong.top` | x-ui / xray 入口 | DNS-only，A 返回 `23.185.200.12` | `server: nginx/1.24.0 (Ubuntu)`，`/` 200 | `127.0.0.1:46585`、`127.0.0.1:10000`、`127.0.0.1:2096` |
| `www.areasong.top` | 门户网站预留域名；旧 Cloudflare Access / Tunnel 入口已下线 | Cloudflare 边缘仍返回 A/AAAA；门户源站未接入 | `server: cloudflare`；当前返回 HTTP 530，不再跳转 `areasong.cloudflareaccess.com` | 暂无；待门户网站接入 |

说明：

- Cloudflare 代理 / DNS-only、TTL、Tunnel 记录基于 Cloudflare 控制台核对；公开 DNS 和 HTTP 响应头用于交叉验证。
- `log.areasong.top` 的 x-ui 管理面板隐藏路径不进入台账；Nginx 配置核查时已脱敏。
- `www.areasong.top` 的旧 Cloudflare Access Application 与 Tunnel/Public Hostname 已由用户在 Cloudflare 控制台删除；当前不再作为旧入口使用，预留给后续门户网站。
- `ops.areasong.top` 的目标配置已进入仓库，但公开 DNS、HTTP 响应和 Cloudflare 控制面均未完成生产验收，不能据此条目认定已上线。

## 源站证书策略

| 源站域名 | Nginx 证书 | 源站 SNI 证书 | 有效期 | 策略 |
| --- | --- | --- | --- | --- |
| `resume.areasong.top` | `/etc/ssl/cf/top/origin.pem` | Cloudflare Origin CA，SAN `*.areasong.top`、`areasong.top` | 2026-07-01 至 2041-06-27 | Cloudflare 代理域名使用 Origin Certificate |
| `sorryiossearch.areasong.top` | `/etc/ssl/cf/top/origin.pem` | Cloudflare Origin CA，SAN `*.areasong.top`、`areasong.top` | 2026-07-01 至 2041-06-27 | Cloudflare 代理域名使用 Origin Certificate |
| `monitor.areasong.top` | `/etc/ssl/cf/top/origin.pem` | Cloudflare Origin CA，SAN `*.areasong.top`、`areasong.top` | 2026-07-01 至 2041-06-27 | Cloudflare 代理域名使用 Origin Certificate |
| `ops.areasong.top` | 计划 `/etc/ssl/cf/top/origin.pem` | 现有 wildcard Origin CA 覆盖目标域名 | 2026-07-01 至 2041-06-27 | **计划/待验收**：Nginx 站点安装后仅允许 Cloudflare 官方 CIDR 回源 |
| `forge.areasong.top` | `/etc/ssl/cf/top/origin.pem` | Cloudflare Origin CA，SAN `*.areasong.top`、`areasong.top` | 2026-07-01 至 2041-06-27 | Cloudflare 代理域名使用 Origin Certificate |
| `cpa.areasong.top` | `/etc/letsencrypt/live/cpa.areasong.top/fullchain.pem` | Let's Encrypt，SAN `cpa.areasong.top` | 2026-07-02 至 2026-09-30 | DNS-only 直连域名使用公开可信证书 |
| `log.areasong.top` | `/etc/letsencrypt/live/log.areasong.top/fullchain.pem` | Let's Encrypt，SAN `log.areasong.top` | 2026-07-01 至 2026-09-29 | DNS-only 直连域名使用公开可信证书 |

私钥路径不写入台账明细；原则是 root-only 存放，不进入 Git。

## 公网证书表现

| 域名 | 公网证书 issuer | 公网证书 SAN | 说明 |
| --- | --- | --- | --- |
| `resume.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书 |
| `sorryiossearch.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书 |
| `monitor.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书 |
| `ops.areasong.top` | 待 Cloudflare 代理生效后验收 | 目标为 `*.areasong.top`、`areasong.top` | **计划状态**，当前不记录为已观察到的公网证书 |
| `forge.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书 |
| `www.areasong.top` | Let's Encrypt `YE2` | `*.areasong.top`、`areasong.top` | Cloudflare 边缘证书；旧 Access/Tunnel 入口已下线，预留门户网站 |
| `cpa.areasong.top` | Let's Encrypt `YE1` | `cpa.areasong.top` | 源站直连证书 |
| `log.areasong.top` | Let's Encrypt `YE2` | `log.areasong.top` | 源站直连证书 |

## 续期与维护

| 项目 | 当前状态 |
| --- | --- |
| Cloudflare Origin Certificate | 控制台创建人和轮换负责人均为 `as`；当前用途为 `resume.areasong.top`、`sorryiossearch.areasong.top`、`monitor.areasong.top`、`forge.areasong.top`，目标增加 `ops.areasong.top`；长期有效至 2041-06-27；过期前 180/90/30/7 天提醒 |
| Let's Encrypt / acme.sh | root crontab 存在每日 acme.sh cron：`13 23 * * *` |
| 监控 | Blackbox 已监控公网 HTTPS 可用性和公网证书临期；本机 Cloudflare Origin Certificate 文件已接入 textfile metrics、Prometheus 告警、Alertmanager 邮件提醒和 Grafana `LosAngeles Certificates and Cloudflare` Dashboard |

## 治理元数据

| 对象 | 用途 | 创建人 | 负责人 | 提醒 / 处置 |
| --- | --- | --- | --- | --- |
| Cloudflare Origin Certificate `*.areasong.top` / `areasong.top` | `resume.areasong.top`、`sorryiossearch.areasong.top`、`monitor.areasong.top`、`forge.areasong.top` 的代理回源 TLS 证书 | `as` | `as` | 2041-06-27 到期；Prometheus 已落地 180/90/30/7 天分级提醒，30 天内安排轮换，7 天内按紧急处理 |
| Access Application `Grafana - monitor.areasong.top`（ID `8f78fba9-dadd-4ab0-ab18-41e895e7a32f`） | `monitor.areasong.top` 人员和自动化身份边界 | `as` | `as` | 仅允许 `song80184@gmail.com` 通过 OTP；会话 6 小时；策略或人员变化时立即复核 |
| Access Application `AreaSong Ops - ops.areasong.top`（计划） | AreaSong Ops 人员身份边界 | 待创建 | `as` | **待单独批准和验收**：仅允许 `song80184@gmail.com` OTP、会话 6 小时；不复用 Grafana service token、不创建 Bypass |
| Service Token `github-actions-grafana-health`（ID `f9008337-dab4-46ec-8802-c17ea2739634`） | 仅供 `AreaSong/ops` 外部可用性工作流读取 Grafana `/api/health` | `as` | `as` | 2027-07-29 到期；2027-06-29 前轮换；GitHub 仅保存 `CF_ACCESS_CLIENT_ID` / `CF_ACCESS_CLIENT_SECRET` secrets |
| Cloudflare Tunnel `hWin` / `www.areasong.top` | 旧 Cloudflare Access / Tunnel 应用入口 | `as` | `as` | 2026-07-04 已由用户删除 Access Application 与 Tunnel/Public Hostname；LosAngeles 本机未发现 `cloudflared` 进程或 systemd 服务；`www` 预留门户网站 |

## 2026-07-29 Access 与源站治理状态

| 对象 | 目标策略 | 当前权威状态 | 完成证据 |
| --- | --- | --- | --- |
| `monitor.areasong.top` Access Application | self-hosted；仅允许指定运维邮箱 OTP；6 小时会话 | **已生效**：`Grafana - monitor.areasong.top`；人员策略仅包含 `song80184@gmail.com` | 浏览器已完成 OTP 并进入 Grafana；未认证入口仍由 Access 拦截 |
| `ops.areasong.top` Access Application | self-hosted；仅允许 `song80184@gmail.com` OTP；6 小时会话；无 Bypass/service token | **计划/待验收**：尚未创建 DNS、Application 或策略 | 创建属于后续独立生产写操作；须验证未认证 302、错误邮箱拒绝、允许邮箱 OTP、JWT/CSRF 和源站直连 403 |
| 外部监控 service token | 仅允许 GitHub Actions 读取 Grafana health；最小范围；有 owner/到期/轮换 | **已生效**：自动化策略仅允许 `github-actions-grafana-health`；到期 2027-07-29 | Actions secrets 已写入；工作流 #191 六个入口全 200，Grafana 由 service token 返回 200；#192/#193 已完成故障/恢复 Issue 演练 |
| `resume` / `sorryiossearch` / `monitor` / `forge` 源站 | Nginx 仅允许 Cloudflare 官方 IPv4/IPv6 | **生产已生效**（2026-07-21 只读回验）：站点 include `cloudflare-origin-only.conf`；本机 `--resolve` 直连返回 403；Cloudflare 公网路径可达（monitor 302 / forge 307） | 台账 `observed_origin_policy` 已晋升为 `cloudflare-only`；`cpa` / `log` 仍为 DNS-only + `direct` |

Access 配置和 token 创建属于外部控制面写操作。token secret 只在创建时显示，不进入本台账；台账只记录名称、用途、owner、创建日、到期日、轮换日和 GitHub secret 名称。

## Cloudflare 控制台核对

2026-07-04 通过 Cloudflare 控制台只读核对 Zone 配置；2026-07-18 追加核对时 Access Application 与 service token 均为空；2026-07-29 按批准完成 Grafana Access 创建和验证。以下 Zone 表保留 2026-07-04 的 Zone 配置事实边界，Access 的当前事实以上方 2026-07-29 表为准：

| 类别 | 当前状态 | 说明 |
| --- | --- | --- |
| DNS TTL | 全部已存在记录为自动 | `resume`、`sorryiossearch`、`monitor`、`forge` 已代理；`cpa`、`log` 为 DNS-only；`ops` 为尚未创建/验收的目标记录；`www` 的旧 Access/Tunnel 入口已下线，当前仍由 Cloudflare 边缘响应并预留给门户网站。 |
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

- Cloudflare Origin Certificate 的自动化提醒已落地到 Prometheus / Alertmanager / Grafana；如需更强治理，可再补日历或任务系统提醒。
- `www.areasong.top` 的门户网站接入方案仍需确认，包括源站位置、Cloudflare 代理状态、Nginx server block、证书策略、WAF/缓存规则和上线回滚方案。
- `ops.areasong.top` 仍需按独立生产变更完成 Nginx、DNS、Access Application 与仅允许邮箱策略，并在验收后把 inventory 的 lifecycle/observed policy 从计划态晋升为已验证事实。
- 若未来启用 HSTS、Bot Fight Mode、速率限制、WAF 自定义规则或缓存规则，需要先评估对 `log`、`cpa`、`monitor` 等入口的影响。

## `www.areasong.top` 下线记录

2026-07-04 用户确认已在 Cloudflare 控制台删除 `www.areasong.top` 对应的旧 Access Application 和 Tunnel/Public Hostname。

核查结果：

- LosAngeles 本机未发现 `cloudflared` 进程或 systemd 服务。
- `/opt/ops` 中仅存在台账记录，未发现本机业务依赖。
- 公网访问 `https://www.areasong.top/` 不再跳转 `areasong.cloudflareaccess.com`。
- 当前公网表现为 Cloudflare HTTP 530，说明旧入口已不可用，门户网站尚未接入。

后续门户网站接入前，应先确认：

- 门户部署位置：LosAngeles Nginx 静态站、独立应用容器、Vercel/Cloudflare Pages 或其他平台。
- DNS 策略：继续 Cloudflare 代理，或临时 DNS-only 到源站。
- 证书策略：Cloudflare 代理入口使用 Origin Certificate；DNS-only 直连入口使用公开可信证书。
- Nginx / 应用回滚：上线前保留默认站点和回滚配置。

## 当前建议

1. Cloudflare 代理域名继续使用 Cloudflare Origin Certificate；DNS-only 直连域名继续使用 Let's Encrypt。
2. 若未来将 `cpa.areasong.top` 或 `log.areasong.top` 改为 Cloudflare 代理，需要同步更新本台账、Nginx 证书策略和 Prometheus target。
3. `www.areasong.top` 后续接入门户网站时，先完成部署方案、DNS/证书策略、Nginx 配置和回滚方案，再切换生产入口。
4. 继续保持 HSTS、Bot Fight Mode、热链接保护和自定义缓存规则关闭，除非后续有明确业务需求和回滚方案。
