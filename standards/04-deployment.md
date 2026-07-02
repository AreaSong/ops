# 04 服务部署规范

## 标准部署方式

新服务默认使用 **Docker Compose**，目录 `/opt/compose/<项目名>/`。

### Compose 模板要求

```yaml
services:
  my-service:
    image: my-service:1.0.0          # 固定版本 tag，禁止 latest
    container_name: my-service        # 显式命名
    restart: unless-stopped
    volumes:
      - /data/my-service:/data        # 数据卷
    logging:
      driver: json-file
      options:
        max-size: "50m"
        max-file: "3"
    # 健康检查
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
      interval: 30s
      timeout: 5s
      retries: 3
```

### 目录布局

```
/opt/compose/my-service/
├── docker-compose.yml
├── .env                    # 环境变量（不入库，secrets.env 引用）
└── config/                 # 配置文件
```

## systemd 裸部署（例外）

仅以下情况允许：

- 内核模块依赖
- 极端性能需求（需 benchmark 数据支撑）
- 无法用容器化的遗留系统

部署目录：`/opt/apps/<服务名>/`

### systemd 单元模板

```ini
[Unit]
Description=My Service
After=network.target

[Service]
Type=simple
User=my-service
Group=my-service
WorkingDirectory=/opt/apps/my-service
ExecStart=/opt/apps/my-service/bin/my-service
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Nginx 规范

- 配置目录：`/etc/nginx/conf.d/` 或 `/opt/apps/nginx/conf.d/`
- 站点配置：`<域名>.conf`
- 改配置流程：`nginx -t` → `systemctl reload nginx`（不用 restart）
- SSL 证书：Let's Encrypt 或云证书，过期前 30 天告警

## Kubernetes 规范

- 变更用声明式：`kubectl apply -f`
- 显式指定 namespace：`-n <namespace>`
- Deployment 必须配置 liveness/readiness probe
- 资源限制：requests 和 limits 必须设置
- rollout 后验证：`kubectl rollout status deployment/<name> -n <ns>`

## 部署检查清单

新服务部署完成后逐项确认：

- [ ] 目录结构符合规范（/opt/compose 或 /opt/apps）
- [ ] 容器/服务命名规范
- [ ] restart 策略配置
- [ ] 数据卷挂载到 /data/
- [ ] 日志配置（大小限制 + logrotate）
- [ ] 健康检查配置
- [ ] 开机自启验证
- [ ] 备份方案已实施（见 06-backup-dr.md）
- [ ] 台账已更新（inventory/services.yaml）
- [ ] 端口已登记（inventory/ports.md）
- [ ] 监控已接入（见 08-observability.md）

## 有状态服务

部署有状态服务（数据库、缓存、消息队列）**必须同时给出备份方案**，没有备份方案的部署不算完成。

---

修订记录：

- 2026-07-02 初版
