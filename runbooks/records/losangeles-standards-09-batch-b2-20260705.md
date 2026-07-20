# LosAngeles standards/09 批次 B2：Docker 日志基线收敛

更新时间：2026-07-05  
服务器：LosAngeles  
范围：Docker daemon 日志默认策略与 live-restore  
风险级别：中；已重启 Docker daemon，业务容器有短暂维护窗口

## 1. 本批次完成项

### 1.1 Docker daemon 日志轮转默认值

已配置：

- `/etc/docker/daemon.json`

关键配置：

```json
{
  "live-restore": true,
  "log-driver": "json-file",
  "log-opts": {
    "max-file": "5",
    "max-size": "50m"
  }
}
```

目的：

- 避免 Docker `json-file` 日志无限增长导致磁盘被打满。
- 将新建容器默认日志上限控制在约 `50m * 5`。
- 启用 `live-restore`，降低后续 Docker daemon 重启对运行中容器的影响。

注意：

- Docker daemon 默认日志参数主要影响后续新建容器。
- 已存在容器是否立即采用新默认值，取决于容器创建时的日志配置；如需完全一致，需要在维护窗口逐个重建容器。

### 1.2 维护窗口执行

已执行：

```bash
sudo systemctl restart docker
```

脚本在重启前记录运行中容器列表，重启后比对容器恢复情况；如有缺失会尝试 `docker start` 恢复。

### 1.3 验证

已验证：

- `/etc/docker/daemon.json` JSON 语法正确。
- `dockerd --validate --config-file /etc/docker/daemon.json` 通过，或在当前 Docker 版本不支持时完成 JSON 语法校验。
- Docker daemon 重启后可用。
- 重启前运行中的容器已恢复运行。
- `docker info` 显示默认日志驱动为 `json-file`。

## 2. 备份与回滚

变更前配置与容器状态备份位于：

- `/root/ops-change-backups/standards09-b2-<timestamp>/`

如需回滚：

```bash
sudo cp /root/ops-change-backups/standards09-b2-<timestamp>/daemon.json.before /etc/docker/daemon.json
sudo systemctl restart docker
```

如果变更前不存在 `/etc/docker/daemon.json`，则可删除该文件后重启 Docker：

```bash
sudo rm -f /etc/docker/daemon.json
sudo systemctl restart docker
```

## 3. 后续建议

本批次只收敛 daemon 默认值。后续如要完全企业化，可在维护窗口继续：

- 检查每个现有容器的实际 `LogConfig`。
- 对旧容器逐个通过 compose 重建，使其继承新的日志默认值。
- 为关键 compose 增加显式 `logging` 策略，避免未来迁移时依赖 daemon 默认值。

状态：完成。
