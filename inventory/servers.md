# 服务器台账

> 新增/释放机器时更新本表并 git 提交。

| 主机名 | 云 | 区域 | 公网 IP | 内网 IP | 系统 | 运行服务 | 备注 |
|--------|-----|------|---------|---------|------|----------|------|
| （示例）prod-web-01 | 阿里云 | 华东1-杭州 | x.x.x.x | 172.16.x.x | Ubuntu 22.04 | nginx, app | 无独立数据盘 |
| LosAngeles | unknown | unknown | 23.185.200.12 |  | Ubuntu 24.04 | nginx, x-ui, xray, resume-jadeai, sub2api, sub2api-postgres, sub2api-redis, account-vault-web, account-vault-postgres | 无独立数据盘；服务部署在 /root；ufw inactive；SSH root/password 风险；需确认云厂商、region、私网 IP、业务 owner |
