# 新应用接入表

填写日期：
操作人：

## 1. 基础信息

| 项目 | 内容 |
| --- | --- |
| 应用名 | `__APP_NAME__` |
| 应用说明 | `__APP_DESCRIPTION__` |
| 项目仓库 / 来源 |  |
| 项目类型 | 静态站 / Node / Python / Go / Java / Docker 项目 / 其他 |
| 负责人 |  |

## 2. 运行信息

| 项目 | 内容 |
| --- | --- |
| 容器内端口 | `__APP_PORT__` |
| 宿主机本地端口 | `__HOST_PORT__` |
| 启动命令 |  |
| 构建命令 |  |
| 健康检查路径 | `__HEALTH_PATH__` |
| 是否需要文件上传 / 持久化目录 | 否 / 是，路径： |

## 3. 域名与入口

| 项目 | 内容 |
| --- | --- |
| 域名 | `__DOMAIN__` |
| Cloudflare 代理 | 橙云 / 灰云 |
| 证书策略 | Cloudflare Origin Certificate / Let's Encrypt |
| Nginx 配置文件 | `/etc/nginx/sites-available/__DOMAIN__.conf` |

## 4. 依赖

| 依赖 | 是否需要 | 说明 |
| --- | --- | --- |
| Postgres | 否 / 是 |  |
| Redis | 否 / 是 |  |
| 外部 API | 否 / 是 |  |
| 对象存储 | 否 / 是 |  |

## 5. 环境变量

只写变量名和用途，不写真实值。

| 变量名 | 用途 | 是否敏感 |
| --- | --- | --- |
| `PORT` | 应用监听端口 | 否 |
|  |  |  |

真实值保存到：

```text
/etc/__APP_NAME__/__APP_NAME__.env
```

## 6. 备份与恢复

| 项目 | 内容 |
| --- | --- |
| 是否有持久化数据 | 否 / 是 |
| 数据目录 |  |
| 是否纳入 `/var/backups/ops` | 否 / 是 |
| 恢复验证方式 |  |

## 7. 发布与回滚

| 项目 | 内容 |
| --- | --- |
| 发布方式 | 手工 Docker Compose / GitHub Actions / 其他 |
| 回滚方式 | 上一镜像 / 上一代码目录 / Git revert / 其他 |
| 回滚验证 |  |

## 8. 上线验收

- [ ] 本机 health 通过。
- [ ] 公网 HTTPS 通过。
- [ ] Nginx `nginx -t` 通过。
- [ ] 容器日志无启动错误。
- [ ] Grafana / Blackbox 探针已记录或明确暂缓。
- [ ] 备份策略已记录或明确无持久化数据。
- [ ] `/opt/ops` 文档已更新并提交。
