# 服务器台账

> 新增/释放机器时更新本表并 git 提交。

| 主机名 | 云 / 网络归属 | 区域 | 公网 IP | 内网 IP | Owner | 系统 | 运行服务 | 备注 |
|--------|-----|------|---------|---------|-------|------|----------|------|
| （示例）prod-web-01 | 阿里云 | 华东1-杭州 | x.x.x.x | 172.16.x.x | ops | Ubuntu 22.04 | nginx, app | 无独立数据盘 |
| LosAngeles | CrystalClear Solutions LLC；ASN 信号 AS8796 FASTNET DATA INC | us-ca-los-angeles | 23.185.200.12 | 无主机级 RFC1918 私网 IP | as | Ubuntu 24.04 | nginx, x-ui, xray, resume-jadeai, areaforge-web, areaforge-postgres, sub2api, sub2api-postgres, sub2api-redis, account-vault-web, account-vault-postgres | 无独立数据盘；ARIN RDAP：CRYSTAL / 23.185.200.0/24；ipinfo：Los Angeles, California, US；KVM/QEMU + cloud-init nocloud；ens3 直接使用公网 23.185.200.12/24；Docker bridge 使用 172.17/18/19/20/21 网段；服务已主要规范到 /opt/services；旧 /root/JadeAI 与 /root/sorryiosSearch 已归档并删除；云厂商控制台 `https://server.zgocloud.cc/`，实例名 `LosAngeles`，MFA enabled，邮箱/手机号可用，主账号未共用，无 API Key；厂商无安全组/云防火墙/网络规则、快照、云审计/安全通知，账单/到期治理暂缓；UFW active: allow 22/80/443, default deny incoming；SSH 已禁用 root/password，仅 as key 登录；Fail2ban sshd jail enabled；本机备份和 R2 异地备份 enabled；监控与告警 enabled。 |
