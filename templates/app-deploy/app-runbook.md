# __APP_NAME__ 应用运维记录

更新时间：
服务器：`LosAngeles`
应用：`__APP_NAME__`
域名：`__DOMAIN__`

## 1. 基础信息

| 项目 | 内容 |
| --- | --- |
| 应用说明 | `__APP_DESCRIPTION__` |
| 服务目录 | `/opt/services/__APP_NAME__` |
| 环境变量 | `/etc/__APP_NAME__/__APP_NAME__.env` |
| Compose 文件 | `/opt/services/__APP_NAME__/docker-compose.yml` |
| Nginx 配置 | `/etc/nginx/sites-available/__DOMAIN__.conf` |
| 本机端口 | `127.0.0.1:__HOST_PORT__` |
| 容器端口 | `__APP_PORT__` |
| Health | `https://__DOMAIN____HEALTH_PATH__` |

## 2. 启停

```bash
cd /opt/services/__APP_NAME__
sudo docker compose ps
sudo docker compose up -d
sudo docker compose restart app
sudo docker compose logs app --tail=100
```

## 3. 验证

```bash
curl -fsS http://127.0.0.1:__HOST_PORT____HEALTH_PATH__
curl -fsS https://__DOMAIN____HEALTH_PATH__
sudo nginx -t
```

## 4. 备份

| 数据 | 路径 | 是否备份 | 说明 |
| --- | --- | --- | --- |
| 应用数据 | `/var/lib/__APP_NAME__` | 待确认 |  |

## 5. 监控

| 项目 | 状态 |
| --- | --- |
| Blackbox HTTPS | 待接入 / 不需要 |
| Grafana 面板 | 待接入 / 不需要 |
| Alertmanager 告警 | 待接入 / 不需要 |
| 日志采集 | 待接入 / 不需要 |

## 6. 回滚

记录上一版本：

```text
image / commit:
compose:
nginx:
```

回滚步骤：

1. 停止新版本容器。
2. 恢复上一版本 compose / image / code。
3. 启动应用。
4. 验证本机 health 和公网 HTTPS。
5. 必要时恢复 Nginx 配置并 reload。
