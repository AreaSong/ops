# LosAngeles standards09 D2 云侧控制面治理清单

日期：2026-07-05  
范围：VPS / 云厂商控制台、Cloudflare 控制台、账单与审计治理  
目标：把服务器内已能确认的事实、Cloudflare 已完成项、以及必须在外部控制台人工确认的项目分开管理。

## 结论

状态：控制台人工确认已完成；云账号基础安全完成；账单/到期按用户要求暂缓；安全组/云防火墙、快照、云审计/安全通知为当前厂商控制台不可用或未提供能力，已记录为风险接受与补偿控制。

服务器侧已经完成了大量主机内治理：SSH、UFW、Fail2ban、Nginx、备份、R2、监控、告警、容器资源/日志/镜像/安全参数、备份恢复演练、业务 Compose 受控副本等。

Cloudflare / DNS / 证书台账已有基础闭环，见：

- `inventory/cloudflare-areasong-top.md`
- `runbooks/losangeles-current-status.md`
- `runbooks/losangeles-hardening-progress.md`

本次 D2 聚焦剩余的“服务器内查不到或不能代替控制台确认”的控制面事项。2026-07-05 用户已在云厂商控制台人工核对并回填结果。

## 2026-07-05 云厂商控制台确认结果

确认来源：用户在云厂商控制台人工核对后反馈。未记录任何密码、恢复码、API secret 或 token。

| 项目 | 确认结果 | 状态 |
| --- | --- | --- |
| 控制台 | `https://server.zgocloud.cc/` | 已记录 |
| 实例名称 | `LosAngeles` | 已确认 |
| 主账号 MFA / 两步验证 | 已开启 | 完成 |
| 账号绑定邮箱和手机号 | 仍可用 | 完成 |
| 主账号共用情况 | 无其他人共用主账号 | 完成 |
| 安全组 / 云防火墙 / 网络规则 | 厂商控制台无此概念或页面 | 厂商能力缺口，主机侧补偿 |
| 账单与到期 | 用户明确“先不管” | 暂缓 |
| 快照 | 无 | 厂商能力缺口或未配置，备份体系补偿 |
| 审计与安全通知 | 无 | 厂商能力缺口，主机侧与 Cloudflare 侧补偿 |
| API Key | 无 | 完成，无待清理 key |

## 治理状态汇总

| 治理项 | 当前状态 | 补偿措施 / 后续 |
| --- | --- | --- |
| 云账号基础安全 | 完成 | MFA 已开启；邮箱/手机号可用；主账号未共用。 |
| 资源归属 | 完成 | 控制台实例名为 `LosAngeles`；inventory 已记录 owner/provider/region。 |
| API Key | 完成 | 当前无 API Key；后续如新增必须最小权限、标注用途和轮换日期。 |
| 安全组 / 云防火墙 | 风险接受 | 厂商无此能力；以 UFW 默认拒绝入站、仅放行 22/80/443、Fail2ban、端口收敛和监控告警作为补偿。 |
| 快照 | 风险接受 | 当前无快照；以本机备份、R2 异地备份、本机/R2/应用级恢复演练作为补偿。若厂商后续支持快照，优先开启每周快照和高风险操作前手动快照。 |
| 云审计 / 安全通知 | 风险接受 | 厂商无此能力；以主机日志、Fail2ban、UFW/Nginx 指标、Loki/Grafana/Alertmanager、Cloudflare 控制台审计能力作为补偿。 |
| 账单 / 到期 | 暂缓 | 用户本轮明确先不处理；后续应单独补自动续费、到期提醒、余额/扣费失败通知。 |

## 服务器侧已确认事实

LosAngeles inventory 当前记录：

- provider：`crystalclear-solutions-llc`
- region：`us-ca-los-angeles`
- public IP：`23.185.200.12`
- owner：`as`
- OS：Ubuntu 24.04
- 主机级私网 IP：无
- 虚拟化：KVM/QEMU
- 云初始化线索：`nocloud`
- 当前公网入口：`22/tcp`、`80/tcp`、`443/tcp`
- UFW：默认拒绝入站，仅放行 22/80/443
- 应用与监控端口：均已收敛到本机或 Docker 网络，通过 Nginx / 443 入口暴露

## Cloudflare 当前状态

已记录或已完成：

- `areasong.top` 的 Cloudflare 台账已存在。
- `monitor/resume/sorryiossearch` 使用 Cloudflare 代理与 Origin Certificate。
- `log/cpa` 当前为 DNS-only，使用 Let's Encrypt。
- Cloudflare Origin Certificate 本地证书过期监控已接入 Prometheus / Alertmanager / Grafana。
- R2 异地备份、R2 拉回演练、R2 生命周期策略已完成。
- 旧 `www.areasong.top` / Tunnel `hWin` 入口已下线，`www` 预留后续门户网站。

仍建议控制台复核：

- Cloudflare 账号 MFA 是否开启。
- Cloudflare 账号恢复邮箱、手机号、备用登录方式是否可用。
- R2 API Token / S3 Key 是否为最小权限，是否有轮换负责人和轮换日历。
- Cloudflare 审计日志中最近登录、API token 创建、DNS/WAF/Tunnel/Access 变更是否有异常。
- `www.areasong.top` 后续门户上线前，先补 DNS、证书、Nginx、WAF/缓存和回滚方案。

## VPS / 云厂商控制台治理结果

### P0：账号与访问安全

确认结果：

- 主账号 MFA 已开启。
- 主账号邮箱、手机号、恢复方式有效。
- 没有多人共用主账号进行日常运维。
- 当前无 API Key。

记录方式：

- 本文仅记录确认结果，不提交控制台截图。
- 不要把截图里的敏感 token、完整邮箱验证码、恢复码提交到 Git。

### P0：账单、到期、欠费告警

当前状态：

- 用户本轮明确“先不管”。
- 本项不标记为完成，后续单独处理。

建议基线：

- 到期前 30/14/7/1 天提醒。
- 余额低于一个月费用时提醒。
- 月度费用环比超过 10% 时人工 review。

### P0：安全组 / 云防火墙

确认结果：

- 云服务厂商没有安全组、云防火墙或网络规则概念/页面。
- 无法在云控制面增加外层网络 ACL。

目标策略：

- 入站只允许：
  - `22/tcp`
  - `80/tcp`
  - `443/tcp`
- 出站默认允许，除非云厂商有更细策略要求。
- 不允许数据组件端口公网入站，如 Postgres、Redis、Prometheus、Grafana、Loki 等。
- SSH 当前可继续 `0.0.0.0/0`，但这是风险接受；若未来有固定出口 IP，再在 UFW 收敛到固定来源。

补偿控制：

- UFW 默认拒绝入站，仅放行 22/80/443。
- Fail2ban sshd jail 已启用。
- 公网监听面已收敛到 22/80/443。
- 数据库、Redis、Prometheus、Grafana、Loki、Alertmanager 等未直接暴露公网。

### P1：快照策略

确认结果：

- 当前无快照。

补偿控制：

- 本机备份已完成。
- Cloudflare R2 异地备份已完成。
- 本机备份恢复、R2 拉回恢复、应用级恢复演练已完成。

后续建议：

- 若厂商后续支持快照，开启每周至少 1 次系统盘快照，保留 4 周。
- 高风险操作前手动快照，保留 7-30 天。
- 快照命名包含主机、日期、原因，例如：`LosAngeles-before-maintenance-YYYYMMDD`。
- 快照不是数据库一致性备份的替代品；数据库仍以 Postgres dump / Redis / volume / R2 备份为主。

### P1：云审计与安全告警

确认结果：

- 云厂商控制台无审计与安全通知能力。
- 无法在该厂商侧接入实例高危操作审计或异常安全通知。

补偿控制：

- 主机侧 SSH / Fail2ban / UFW / Nginx 日志已接入 metrics、Loki、Grafana 和 Alertmanager。
- Fail2ban 封禁 IP 明细与 IP 归属增强已接入 Grafana。
- Cloudflare 代理域名、R2、证书策略已有独立台账与告警。

### P2：资源标签与资产归属

确认结果：

- 实例名称：`LosAngeles`
- owner：`as`
- environment：`prod`
- role：`web,docker-app,proxy`
- cost-center / project：暂不设置

目标：后续账单 review、资源清理和交接时能知道资源归属。

## 当前不要做的事

- 不要直接在控制台释放实例、释放 IP、删除磁盘、删除快照。
- 当前厂商无安全组/云防火墙能力；不要把这项误判为服务器内漏配。
- 不要把云账号密码、MFA 恢复码、API token、R2 secret、SMTP 授权码写入 Git。
- 不要把 `www.areasong.top` 切到生产入口，除非门户网站部署、证书、Nginx、Cloudflare 规则和回滚方案都已准备好。

## 后续处理建议

1. 账单、到期、欠费提醒用户本轮暂缓；建议后续单独处理，不和服务器内配置混在一起。
2. 如果后续更换云厂商或该厂商新增快照/审计/安全组能力，应重新评估 D2。
3. 如未来新增 API Key，应记录用途、权限范围、创建日期、轮换负责人和轮换周期；secret 不进 Git。
4. 如未来有固定出口 IP，可在 UFW 上继续收敛 SSH 来源。
