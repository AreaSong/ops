# __APP_NAME__ 部署检查清单

## 1. 部署前

- [ ] 已填写 `app-intake.md`。
- [ ] 已确认域名 `__DOMAIN__` 的 Cloudflare 代理状态。
- [ ] 已确认宿主机端口 `127.0.0.1:__HOST_PORT__` 未被占用。
- [ ] 已确认项目能在本地或构建环境成功构建。
- [ ] 已确认真实 secret 只写入 `/etc/__APP_NAME__/__APP_NAME__.env`。
- [ ] 如有持久化数据，已确认数据目录和备份策略。

## 2. 安装文件

```bash
sudo install -d -m 0755 -o root -g root /opt/services/__APP_NAME__
sudo install -d -m 0755 -o root -g root /opt/services/__APP_NAME__/app
sudo install -d -m 0750 -o root -g root /etc/__APP_NAME__
sudo install -d -m 0755 -o root -g root /var/lib/__APP_NAME__
```

复制项目源码到：

```text
/opt/services/__APP_NAME__/app
```

复制并填写环境变量：

```bash
sudo cp env.example /etc/__APP_NAME__/__APP_NAME__.env
sudo chown root:root /etc/__APP_NAME__/__APP_NAME__.env
sudo chmod 0600 /etc/__APP_NAME__/__APP_NAME__.env
```

## 3. 启动应用

```bash
cd /opt/services/__APP_NAME__
sudo docker compose config
sudo docker compose up -d --build
sudo docker compose ps
```

本机验证：

```bash
curl -fsS http://127.0.0.1:__HOST_PORT____HEALTH_PATH__
sudo docker logs __APP_NAME__ --tail=100
```

## 4. 接入 Nginx

```bash
sudo cp nginx-site.conf /etc/nginx/sites-available/__DOMAIN__.conf
sudo ln -s /etc/nginx/sites-available/__DOMAIN__.conf /etc/nginx/sites-enabled/__DOMAIN__.conf
sudo nginx -t
sudo systemctl reload nginx
```

公网验证：

```bash
curl -I https://__DOMAIN__/
curl -fsS https://__DOMAIN____HEALTH_PATH__
```

## 5. 接入台账与监控

- [ ] 更新 `/opt/ops/inventory/services.yaml`。
- [ ] 更新 `/opt/ops/inventory/ports.md`。
- [ ] 如需公网探针，更新 Blackbox / Grafana / Alertmanager 配置。
- [ ] 如需备份，更新 `/opt/ops/scripts/backup` 和备份指标。
- [ ] 将本应用运维信息整理到 `app-runbook.md` 或独立 runbook。

## 6. 提交

```bash
sudo git -C /opt/ops status --short
sudo git -C /opt/ops diff --check
sudo git -C /opt/ops add <changed-files>
sudo git -C /opt/ops commit -m "[deploy] add __APP_NAME__ service"
sudo env GIT_SSH_COMMAND='ssh -i /root/.ssh/id_ed25519_ops_github -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes' \
  git -C /opt/ops push origin main
```

## 7. 回滚

- [ ] 记录当前镜像、commit、compose 文件和 Nginx 配置。
- [ ] 回滚应用容器到上一版本。
- [ ] 如 Nginx 变更导致异常，移除站点 symlink 后 `nginx -t && systemctl reload nginx`。
- [ ] 如数据库迁移不可逆，先暂停并按单独恢复方案处理。
