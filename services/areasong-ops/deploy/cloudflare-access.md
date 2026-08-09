# AreaSong Ops Cloudflare Access

## 目标状态

- DNS：`ops.areasong.top`，A 记录指向 LosAngeles，开启 Cloudflare 代理。
- Application：Self-hosted，域名 `ops.areasong.top`。
- Session duration：6 小时。
- Allow policy：仅 `song80184@gmail.com`，登录方式使用 Email OTP。
- 不创建 Bypass policy，不复用 Grafana 的 service token，不开放公共 health 路径。
- Web 端校验 Access JWT 的 issuer、audience、有效期和邮箱；Nginx 头不能替代 JWT。

## 写入 Web 配置

创建 Application 后，从 Overview 读取 Application Audience (AUD)，写入 root-only：

```text
/etc/areasong-ops/web.env
```

固定值：

```dotenv
OPS_ALLOWED_EMAIL=song80184@gmail.com
OPS_ACCESS_ISSUER=https://areasong.cloudflareaccess.com
OPS_ACCESS_AUDIENCE=<AreaSong Ops Application AUD>
OPS_PUBLIC_ORIGIN=https://ops.areasong.top
```

文件必须为 `root:root 0600`，AUD 不是密码，但仍由配置文件统一管理。

## 验收

1. 未登录访问返回 Cloudflare Access `302`，不能到达应用页面。
2. 非允许邮箱无法通过策略。
3. 允许邮箱 OTP 登录后，`/api/session` 返回同一邮箱。
4. 篡改、缺失或过期 JWT 返回 `401`。
5. 写请求缺少同源 Origin 或 CSRF 双提交令牌返回 `403`。
6. 源站 IP 直连被 `cloudflare-origin-only.conf` 拒绝。

Cloudflare Application、DNS 和策略均是外部写操作，生产执行前必须单独批准。
