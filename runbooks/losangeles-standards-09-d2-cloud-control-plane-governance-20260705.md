# LosAngeles standards09 D2 云侧控制面治理清单

日期：2026-07-05  
范围：VPS / 云厂商控制台、Cloudflare 控制台、账单与审计治理  
目标：把服务器内已能确认的事实、Cloudflare 已完成项、以及必须在外部控制台人工确认的项目分开管理。

## 结论

状态：清单已建立，控制台确认待执行。

服务器侧已经完成了大量主机内治理：SSH、UFW、Fail2ban、Nginx、备份、R2、监控、告警、容器资源/日志/镜像/安全参数、备份恢复演练、业务 Compose 受控副本等。

Cloudflare / DNS / 证书台账已有基础闭环，见：

- `inventory/cloudflare-areasong-top.md`
- `runbooks/losangeles-current-status.md`
- `runbooks/losangeles-hardening-progress.md`

本次 D2 聚焦剩余的“服务器内查不到或不能代替控制台确认”的控制面事项。

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

## VPS / 云厂商控制台待确认清单

### P0：账号与访问安全

需要在云厂商控制台确认：

- 主账号 MFA 已开启。
- 主账号邮箱、手机号、恢复方式有效。
- 没有多人共用主账号进行日常运维。
- 若支持子账号 / API Key：
  - 日常操作使用子账号。
  - API Key 最小权限。
  - 无长期闲置、未知来源、未知用途的 API Key。
- 控制台登录历史无异常地域、异常时间、未知设备。

记录方式：

- 截图或文字记录确认日期、确认人、发现项。
- 不要把截图里的敏感 token、完整邮箱验证码、恢复码提交到 Git。

### P0：账单、到期、欠费告警

需要确认：

- 实例是否自动续费或账户余额是否足够。
- 到期提醒、余额不足提醒、扣费失败提醒已发到可用邮箱。
- 若支持预算告警：配置月度预算或费用突增告警。
- 账单联系人不是无人维护的旧邮箱。

建议基线：

- 到期前 30/14/7/1 天提醒。
- 余额低于一个月费用时提醒。
- 月度费用环比超过 10% 时人工 review。

### P0：安全组 / 云防火墙

需要确认云厂商是否有实例外层安全组或云防火墙。

目标策略：

- 入站只允许：
  - `22/tcp`
  - `80/tcp`
  - `443/tcp`
- 出站默认允许，除非云厂商有更细策略要求。
- 不允许数据组件端口公网入站，如 Postgres、Redis、Prometheus、Grafana、Loki 等。
- SSH 当前可继续 `0.0.0.0/0`，但这是风险接受；若未来有固定出口 IP，再收敛到固定来源。

注意：

- 不要在控制台直接关闭 22/80/443；先确认当前 SSH 会话、回滚入口和 VNC/控制台登录方式。
- 任何安全组改动都要记录变更前后规则。

### P1：快照策略

需要确认：

- 当前系统盘是否有自动快照策略。
- 是否能手动创建快照。
- 快照是否和本机备份 / R2 备份互补。

建议基线：

- 每周至少 1 次系统盘快照，保留 4 周。
- 高风险操作前手动快照，保留 7-30 天。
- 快照命名包含主机、日期、原因，例如：`LosAngeles-before-maintenance-YYYYMMDD`。

注意：

- 快照不是数据库一致性备份的替代品；它是灾难恢复和误操作回滚兜底。
- 数据库仍以 Postgres dump / Redis / volume / R2 备份为主。

### P1：云审计与安全告警

需要确认：

- 控制台登录日志可查询。
- 实例重启、关机、销毁、快照、网络规则变更有审计记录。
- 异常登录、暴力破解、恶意流量、挖矿/滥用通知会发送到可用邮箱。
- 若云厂商支持 Webhook / 邮件 / 短信告警，至少开启邮件告警。

### P2：资源标签与资产归属

建议确认或补充：

- 实例名称：`LosAngeles`
- owner：`as`
- environment：`prod`
- role：`web,docker-app,proxy`
- cost-center / project：按个人实际项目填写

目标：后续账单 review、资源清理和交接时能知道资源归属。

## 当前不要做的事

- 不要直接在控制台释放实例、释放 IP、删除磁盘、删除快照。
- 不要未确认回滚方式就修改安全组。
- 不要把云账号密码、MFA 恢复码、API token、R2 secret、SMTP 授权码写入 Git。
- 不要把 `www.areasong.top` 切到生产入口，除非门户网站部署、证书、Nginx、Cloudflare 规则和回滚方案都已准备好。

## 下一步执行方式

推荐下一步由用户在云厂商控制台逐项确认，并把结果按以下格式回填：

```text
云厂商控制台确认结果：
- 主账号 MFA：已开启 / 未开启 / 不支持
- 账单/到期/欠费告警：已开启 / 未开启
- 安全组入站规则：22,80,443 only / 其他
- 自动快照策略：已开启，周期与保留期为 ... / 未开启
- 控制台审计日志：可查询 / 不可查询
- 异常登录或安全告警通知：已开启 / 未开启
- API Key：无 / 有，已确认用途和权限 / 待清理
```

收到确认结果后，再更新：

- `inventory/servers.yaml`
- `runbooks/losangeles-current-status.md`
- 本 D2 runbook
