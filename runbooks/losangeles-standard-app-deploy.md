# LosAngeles 标准应用部署流程

更新时间：2026-07-06
服务器：`LosAngeles`
模板目录：`/opt/ops/templates/app-deploy`

## 1. 目的

本流程用于把一个新项目标准化部署到 LosAngeles。

目标是让每个应用都具备：

- 可预测目录。
- root-only secret 管理。
- Docker Compose 启动。
- Nginx 统一公网入口。
- health 验证。
- 备份、监控和回滚口径。
- `/opt/ops` 文档留痕和 Git 提交。

## 2. 使用方式

每次部署新项目，先复制模板：

```bash
APP=portal
sudo install -d -m 0755 -o root -g root "/opt/services/${APP}"
sudo cp -a /opt/ops/templates/app-deploy/. "/opt/services/${APP}/"
```

然后填写：

```text
/opt/services/${APP}/app-intake.md
/opt/services/${APP}/docker-compose.yml
/opt/services/${APP}/nginx-site.conf
/opt/services/${APP}/deploy-checklist.md
/opt/services/${APP}/app-runbook.md
```

真实环境变量从模板复制到 `/etc`：

```bash
sudo install -d -m 0750 -o root -g root "/etc/${APP}"
sudo cp "/opt/services/${APP}/env.example" "/etc/${APP}/${APP}.env"
sudo chown root:root "/etc/${APP}/${APP}.env"
sudo chmod 0600 "/etc/${APP}/${APP}.env"
```

## 3. 部署前必须确认

| 项目 | 要求 |
| --- | --- |
| 应用名 | 小写字母、数字、短横线 |
| 域名 | 已在 Cloudflare 准备好 |
| 本机端口 | 只监听 `127.0.0.1`，不能和现有服务冲突 |
| Secret | 只放 `/etc/<app>/<app>.env` |
| 数据目录 | 如有持久化数据，放 `/var/lib/<app>` 并确认备份策略 |
| Health | 至少有一个可验证路径 |
| 回滚 | 上线前明确上一版本恢复方式 |

## 4. 标准路径

```text
/opt/services/<app>/app
/opt/services/<app>/docker-compose.yml
/etc/<app>/<app>.env
/var/lib/<app>
/etc/nginx/sites-available/<domain>.conf
```

## 5. 标准启动

```bash
cd /opt/services/<app>
sudo docker compose config
sudo docker compose up -d --build
sudo docker compose ps
```

本机验证：

```bash
curl -fsS http://127.0.0.1:<host-port><health-path>
sudo docker logs <app> --tail=100
```

## 6. 标准 Nginx 接入

```bash
sudo cp /opt/services/<app>/nginx-site.conf /etc/nginx/sites-available/<domain>.conf
sudo ln -s /etc/nginx/sites-available/<domain>.conf /etc/nginx/sites-enabled/<domain>.conf
sudo nginx -t
sudo systemctl reload nginx
```

公网验证：

```bash
curl -I https://<domain>/
curl -fsS https://<domain><health-path>
```

## 7. 监控与备份

默认要求：

- 应用必须能被本机 curl 验证。
- 公网域名必须能返回预期 HTTP 状态。
- 有持久化目录时，必须明确是否纳入 `/var/backups/ops`。
- 需要长期运行的业务入口应加入 Blackbox 探针。
- 关键应用应在 Grafana 和 Alertmanager 中有可见信号。

如果是纯静态站或无状态服务，可以记录为“无持久化数据，不需要数据库备份”。

## 8. 文档留痕

部署完成后至少更新：

- `/opt/ops/inventory/services.yaml`
- `/opt/ops/inventory/ports.md`
- 应用自己的 `app-runbook.md` 或独立 runbook

如新增备份、监控、Nginx 配置副本，也要同步到 `/opt/ops` 对应目录。

## 9. Git 提交

```bash
sudo git -C /opt/ops status --short
sudo git -C /opt/ops diff --check
sudo git -C /opt/ops add <changed-files>
sudo git -C /opt/ops commit -m "[deploy] add <app> service"
sudo env GIT_SSH_COMMAND='ssh -i /root/.ssh/id_ed25519_ops_github -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes' \
  git -C /opt/ops push origin main
```

## 10. 不做什么

- 不把 `.env`、token、数据库密码、私钥放进 Git。
- 不让应用直接监听公网 `0.0.0.0`。
- 不跳过 `nginx -t`。
- 不跳过部署后 health 验证。
- 不在没有回滚方案时执行数据库迁移。

## 11. 每次部署时给 Codex 的信息

以后你可以直接提供：

```text
应用名：
域名：
项目路径或仓库：
项目类型：
容器内端口：
宿主机本地端口：
健康检查路径：
启动命令 / 构建命令：
环境变量名：
是否需要 Postgres / Redis：
是否有持久化目录：
是否要接入备份：
是否要接入 Grafana / 告警：
```

如果项目里已有 `Dockerfile`、`docker-compose.yml`、`.env.example` 或 `README.md`，优先按项目现有方式部署；本模板用于补齐服务器侧规范。
