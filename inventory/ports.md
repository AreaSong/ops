# 端口分配表

> 每次防火墙/安全组放行端口时更新本表并 git 提交。

| 端口 | 协议 | 服务 | 所在机器 | 放行范围 | 备注 |
|------|------|------|----------|----------|------|
| 22 | TCP | SSH | 全部 | 限制来源 IP | 密钥登录 |
| （示例）443 | TCP | nginx | prod-web-01 | 0.0.0.0/0 | 公网 HTTPS |
| 80 | TCP | nginx | LosAngeles | 0.0.0.0/0 | 公网 HTTP，反代多个域名 |
| 443 | TCP | nginx | LosAngeles | 0.0.0.0/0 | 公网 HTTPS，反代多个域名；log.areasong.top 已新增 /sub/ 订阅反代到本机 2096 |
| 2096 | TCP | x-ui | LosAngeles | 127.0.0.1 | 本机订阅分享后端；公网访问统一通过 Nginx 443 /sub/ |
| 46585 | TCP | x-ui | LosAngeles | 127.0.0.1 | 本机 x-ui 面板后端；Nginx direct.log.areasong.top 反代相关 |
| 10000 | TCP | xray | LosAngeles | 127.0.0.1 | 本机 xray WebSocket 后端；公网访问通过 Nginx 443 /as |
| 11111 | TCP | xray | LosAngeles | 127.0.0.1 | 本机监听 |
| 62789 | TCP | xray | LosAngeles | 127.0.0.1 | 本机监听 |
| 2082 | TCP | resume-jadeai | LosAngeles | 127.0.0.1 | 映射到容器 3000，Nginx 反代 |
| 8392 | TCP | account-vault-web | LosAngeles | 127.0.0.1 | 映射到容器 3001，Nginx 反代 |
| 25432 | TCP | account-vault-postgres | LosAngeles | 127.0.0.1 | 映射到容器 5432，仅本机访问 |
| 8080 | TCP | sub2api | LosAngeles | 127.0.0.1 | 映射到容器 8080，Nginx 反代 |
| 5432 | TCP | sub2api-postgres | LosAngeles | docker-network | 仅容器网络暴露 |
| 6379 | TCP | sub2api-redis | LosAngeles | docker-network | 仅容器网络暴露 |
