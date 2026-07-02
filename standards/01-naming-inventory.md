# 01 命名与台账规范

## 主机命名

格式：`<环境>-<角色>-<序号>`

| 字段 | 取值 | 示例 |
|------|------|------|
| 环境 | prod / test / staging | prod |
| 角色 | web / db / cache / ops / k8s / monitor | web |
| 序号 | 两位数字 | 01 |

示例：`prod-web-01`、`test-db-01`、`prod-monitor-01`

## 服务与容器命名

- 小写连字符：`my-service`、`redis-cache`
- 容器名 / Compose 服务名与目录名一致
- 禁止 Docker 默认随机名上生产

## 云资源命名

两朵云统一模式：`<环境>-<角色>-<用途>`

| 资源类型 | 示例 |
|----------|------|
| ECS/CVM 实例 | prod-web-01 |
| 安全组 | prod-web-sg |
| OSS/COS 存储桶 | prod-backup-aliyun |
| SLB/CLB 负载均衡 | prod-web-lb |
| RDS/CDB 数据库 | prod-mysql-01 |

## 端口分配

- 端口分配记录在 `inventory/services.yaml` 的 `ports` 字段
- 人类可读摘要保留在 `inventory/ports.md`
- 每次防火墙/安全组放行端口时同步更新并 git 提交

## 标签约定

云资源统一标签（阿里云 Tag / 腾讯云 Tag）：

| 标签键 | 说明 | 示例 |
|--------|------|------|
| env | 环境 | prod |
| role | 角色 | web |
| owner | 负责人 | ops |
| project | 所属项目 | main-app |

## 台账维护

### 结构化台账（机器可读）

- `inventory/servers.yaml`：主机清单
- `inventory/services.yaml`：服务与端口

### 人类可读摘要

- `inventory/servers.md`：表格视图
- `inventory/ports.md`：端口分配表

### 维护规则

1. **新增机器**：servers.yaml + servers.md 同步更新
2. **新增服务/端口**：services.yaml + ports.md 同步更新
3. **释放资源**：从台账删除并 git 提交，注明释放日期
4. **变更后**：git commit message 格式 `[inventory] 描述变更内容`

### Ansible 消费

`ansible/inventory/` 从 `servers.yaml` 生成，见 `ansible/README.md`。

---

修订记录：

- 2026-07-02 初版
