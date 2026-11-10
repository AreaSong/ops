# Coolify 适配器集成指南

## 概述

`coolify-service-v1` 适配器为已托管在 Coolify 平台上的服务提供受信的生命周期管理能力，通过 Coolify 官方 API v1 执行启动、停止、重启和健康检查操作。

## 设计原则

### 安全第一

1. **凭证隔离**：API token 必须存储在权限为 `0[0-7]00` 的专用文件中（不可被组或其他用户读取）
2. **内存保护**：token 仅在进程内存中读取，从不写入日志、argv、环境变量或生成的脚本
3. **错误脱敏**：所有错误输出自动清除 Bearer token 和 Authorization 头信息
4. **HTTPS 优先**：Coolify endpoint 必须使用 https（测试环境可用 http）

### 功能边界

- **已支持**：inspect（状态检查）、restart、start、stop、依赖服务状态追踪
- **明确拒绝**：
  - `backup`/`restore`：必须使用宿主级备份脚本，不通过 Coolify API
  - `prepare`/`update`/`rollback`：需要与 AreaForge 签名发布链集成，当前未实现
  - 流量管理（`drain`/`resume-traffic`）：宿主 Nginx 仍独立管理
- **已验证**：固定 digest、有界超时、依赖顺序、异步状态等待、错误恢复

### 运维约束

1. **固定 digest**：`latestAllowed: false`（生产环境强制）防止无意的自动更新
2. **有界等待**：
   - API 调用超时：10-30秒（可配置）
   - 状态等待超时：120-180秒（重启/启动/停止）
   - 健康检查超时：120秒
3. **依赖保护**：操作期间自动验证依赖服务（如数据库）状态未变化
4. **并发锁**：由上层 orchestrator 实现，适配器本身不处理并发控制

## 配置

### services.json Schema

```json
{
  "schemaVersion": 4,
  "adapters": {
    "coolify-service-v1": {
      "path": "/usr/local/libexec/areasong-ops/adapters/coolify-service.sh",
      "allowedTypes": ["service"]
    }
  },
  "services": {
    "areasong-ops": {
      "name": "areasong-ops",
      "displayName": "AreaSong Ops Web",
      "template": "coolify-service-v1",
      "adapterRef": "coolify-service-v1",
      "runtime": {
        "coolifyEndpoint": "https://coolify.losangeles.internal",
        "serviceUuid": "6oqw6kcjzweijv6r9ux71zoe",
        "tokenFile": "/etc/areasong-ops/secrets/coolify.token",
        "healthUrl": "http://127.0.0.1:3200/health",
        "latestAllowed": false,
        "dependencyServiceUuids": []
      },
      "actions": {
        "inspect": { "enabled": true, "risk": "read_only" },
        "restart": { "enabled": true, "risk": "medium" },
        "start": { "enabled": true, "risk": "medium" },
        "stop": { "enabled": true, "risk": "medium" }
      }
    }
  }
}
```

### 配置字段说明

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `coolifyEndpoint` | string | 是 | Coolify API 地址，必须以 `http://` 或 `https://` 开头 |
| `serviceUuid` | string | 是 | Coolify 服务 UUID，24位小写字母数字（如 Coolify UI 中显示） |
| `tokenFile` | string | 是 | API token 文件绝对路径，文件权限必须为 `0[0-7]00` |
| `healthUrl` | string | 是 | 应用健康检查端点（通常是宿主机 localhost 地址） |
| `latestAllowed` | boolean | 否 | 是否允许 Coolify 自动拉取 latest tag（生产环境应设为 `false`） |
| `dependencyServiceUuids` | array | 否 | 依赖服务 UUID 列表（如数据库），操作期间会验证其状态未变化 |

## 生产部署步骤

### 1. 准备 Coolify 环境

在 Coolify UI 中：

1. 创建或定位目标服务，记录其 UUID（24位字符串）
2. 生成 API token（Settings → API → Create Token）
3. 验证服务配置：
   - Proxy 设置为 NONE（宿主 Nginx 处理公网流量）
   - 设置健康检查端点（如有）
   - 禁用自动部署（避免与 ops 系统冲突）

### 2. 安全配置 Token

在 LosAngeles 主机上：

```bash
# 创建 token 文件并设置安全权限
sudo mkdir -p /etc/areasong-ops/secrets
sudo touch /etc/areasong-ops/secrets/coolify.token

# 写入 token（替换为实际 token）
echo "your-coolify-api-token-here" | sudo tee /etc/areasong-ops/secrets/coolify.token

# 设置权限（仅 root 可读）
sudo chmod 400 /etc/areasong-ops/secrets/coolify.token
sudo chown root:root /etc/areasong-ops/secrets/coolify.token

# 验证权限
ls -l /etc/areasong-ops/secrets/coolify.token
# 应显示：-r-------- 1 root root ... coolify.token
```

**安全警告**：
- 不要将 token 写入 Git 仓库
- 不要在命令行历史中保留 token（使用 `history -d` 或从文件读取）
- 不要通过环境变量传递 token（适配器从文件读取）

### 3. 注册到 services.json

编辑生产配置文件 `/etc/areasong-ops/services.json`：

```bash
sudo vi /etc/areasong-ops/services.json
```

添加或更新服务配置：

```json
{
  "services": {
    "areasong-ops": {
      "name": "areasong-ops",
      "objectId": "service:areasong-ops",
      "template": "coolify-service-v1",
      "adapterRef": "coolify-service-v1",
      "runtime": {
        "coolifyEndpoint": "https://coolify.losangeles.internal",
        "serviceUuid": "6oqw6kcjzweijv6r9ux71zoe",
        "tokenFile": "/etc/areasong-ops/secrets/coolify.token",
        "healthUrl": "http://127.0.0.1:3200/health",
        "latestAllowed": false,
        "dependencyServiceUuids": []
      },
      "actions": {
        "inspect": {
          "name": "inspect",
          "enabled": true,
          "risk": "read_only",
          "steps": ["inspect"]
        },
        "restart": {
          "name": "restart",
          "enabled": true,
          "risk": "medium",
          "steps": ["preflight", "restart", "health"],
          "confirmationTemplate": "重启 {service}",
          "impact": "通过 Coolify API 重启服务，预计短暂中断",
          "rollback": "重启失败时保持原状态"
        }
      }
    }
  }
}
```

### 4. 验证配置

```bash
# 测试 API 连通性（只读操作）
sudo /usr/local/libexec/areasong-ops/cli service inspect areasong-ops

# 预期输出包含：
# - currentStatus: "running" 或其他 Coolify 状态
# - serviceUuid: "6oqw6kcjzweijv6r9ux71zoe"
```

### 5. 逐步迁移（推荐流程）

对于已在 docker-compose 管理的服务（如 sub2api、account-vault）：

1. **阶段 1：双轨运行**
   - Coolify 服务已启动，但 ops 系统仍使用旧 `compose-service-v1` 适配器
   - 测试 Coolify 服务独立稳定性（监控、备份、手动重启）
   
2. **阶段 2：只读集成**
   - 在 services.json 中添加 Coolify 配置，但仅启用 `inspect` 动作
   - 验证 API 调用、状态查询、依赖追踪正常工作
   
3. **阶段 3：写入操作**
   - 启用 `restart`/`start`/`stop` 动作
   - 在维护窗口测试重启操作
   - 验证健康检查、超时、依赖保护机制
   
4. **阶段 4：停用旧适配器**
   - 停止旧 docker-compose 容器（`docker compose stop service-name`）
   - 设置 `restart: "no"`（防止宿主机重启时自动启动）
   - 保留旧 compose 文件用于应急回滚

## 现有服务迁移示例

### areasong-ops Web（已迁移）

- **Coolify UUID**: `6oqw6kcjzweijv6r9ux71zoe`
- **端口**: 12713:3200
- **旧容器**: `areasong-ops-web-3200-1`（已停止，restart=no）
- **依赖**: 无（Runner、SQLite、cron 仍在宿主机）
- **备份**: 仍通过宿主机 Runner 执行 sqlite_snapshot_verify.py
- **状态**: 可通过 Coolify 适配器管理生命周期

### resume-jadeai（已迁移）

- **Coolify UUID**: `wu3zblpkvdjncfrlkqiavfrc`
- **端口**: 1272:8082
- **数据卷**: `jadeai-data`（external，uid 1000）
- **约束**: 只能一个 writer（旧容器已停止）
- **备份**: 需考虑 SQLite 卷的原子备份

### account-vault（已迁移）

- **Web UUID**: `da5yxpvxkprxivcbkc9g27id`
- **PostgreSQL UUID**: `cmajwelgaiyouyev0pzqk2j2`
- **端口**: Web 1278:3392, PG 12725:5432
- **依赖顺序**: 停止时先停 Web 再停 PG，启动时先启 PG 再启 Web
- **数据卷**: `sorryiossearch_pgdata`（external）
- **重要**: 绝不能同时运行两个 PG 实例在同一卷上

配置示例（带依赖）：

```json
{
  "services": {
    "account-vault-web": {
      "runtime": {
        "serviceUuid": "da5yxpvxkprxivcbkc9g27id",
        "dependencyServiceUuids": ["cmajwelgaiyouyev0pzqk2j2"]
      }
    },
    "account-vault-pg": {
      "runtime": {
        "serviceUuid": "cmajwelgaiyouyev0pzqk2j2",
        "dependencyServiceUuids": []
      }
    }
  }
}
```

### areaforge（待迁移）

**不推荐迁移原因**：

1. 使用签名发布链（digest 验证、prepare 演练、rollback 回滚）
2. 每分钟运行 update-agent timer（固定 compose/env/签名检查）
3. 备份包含复杂的迁移库链（PG dump + 版本关联）
4. 当前 Coolify 适配器不支持 `prepare`/`update`/`rollback` 动作

**保持现状**：继续使用 `areaforge-v1` 专用适配器。

### sub2api（计划迁移）

**适合部分迁移**：

- 仅 Web 服务托管到 Coolify
- PostgreSQL 18.3 + Redis 仍使用 docker-compose（受 exporter 监控）
- 迁移前需要：
  1. 实现 `prepare` 动作（隔离环境演练迁移）
  2. 验证 AUTO_SETUP=true 与既有 config.yaml 的幂等性
  3. 确保 migration runner 的 advisory lock 正确工作（同 digest 可重启）
  4. 更新 backup/restore 脚本以支持混合架构

## API 参考

### Coolify API v1 端点

适配器使用以下 Coolify 官方 API：

| 方法 | 路径 | 用途 |
|------|------|------|
| GET | `/api/v1/services/{uuid}` | 查询服务状态（支持的状态：running, stopped, starting, stopping, unknown） |
| POST | `/api/v1/services/{uuid}/start` | 启动服务（异步操作，返回 202 Accepted） |
| POST | `/api/v1/services/{uuid}/stop` | 停止服务（可能删除容器，异步操作） |
| POST | `/api/v1/services/{uuid}/restart` | 重启服务（异步操作） |

**注意**：
- 所有写入操作（POST）都是异步的，API 返回成功不代表操作完成
- 适配器会主动轮询服务状态直到达到期望状态或超时
- `stop` 操作可能删除容器（Coolify 行为），不能依赖容器仍存在且状态为 exited

### 适配器动作映射

| 动作 | 阶段 | Coolify API | 额外验证 |
|------|------|-------------|----------|
| `inspect:inspect` | - | GET /services/{uuid} | 无 |
| `restart:preflight` | 验证前 | GET /services/{uuid}（依赖） | 记录依赖基线 |
| `restart:restart` | 执行 | POST /services/{uuid}/restart | 等待状态变为 running |
| `restart:health` | 验证后 | GET /services/{uuid} + 健康端点 | 验证依赖未变化 |
| `start:start` | 执行 | POST /services/{uuid}/start | 等待状态变为 running |
| `start:health` | 验证后 | GET /services/{uuid} + 健康端点 | 验证依赖未变化 |
| `stop:stop` | 执行 | POST /services/{uuid}/stop | 等待状态变为 stopped |
| `stop:health` | 验证后 | GET /services/{uuid} | 验证依赖未变化 |

## 故障排查

### Token 权限错误

```
ERROR: token file must have mode 0[0-7]00 (currently 644, readable by group or others)
```

**解决**：`sudo chmod 400 /etc/areasong-ops/secrets/coolify.token`

### API 超时

```
ERROR: timeout waiting for service 6oqw6kcjzweijv6r9ux71zoe to reach state running after 120s
```

**可能原因**：
1. Coolify 平台负载高，容器启动慢
2. 镜像拉取时间过长
3. 应用启动阻塞（数据库连接、初始化任务）

**排查步骤**：
1. 在 Coolify UI 查看服务日志
2. 检查宿主机 `docker ps` 和 `docker logs`
3. 验证健康检查端点是否正确（`curl http://127.0.0.1:3200/health`）

### 依赖状态变化

```
ERROR: dependency service state changed unexpectedly
```

**原因**：操作期间依赖服务（如数据库）被重启或状态改变。

**解决**：
1. 等待依赖服务稳定后重试
2. 检查是否有其他自动化任务干扰（Coolify 自动部署、定时任务）
3. 确认 `dependencyServiceUuids` 配置正确

### Coolify API 认证失败

```
ERROR: API returned HTTP 401: {"error":"Unauthorized"}
```

**原因**：Token 无效或过期。

**解决**：
1. 在 Coolify UI 重新生成 token
2. 更新 `/etc/areasong-ops/secrets/coolify.token`
3. 验证 token 没有前后空格（`cat coolify.token | wc -c` 应该是 token 长度）

## 限制与未来工作

### 当前限制

1. **不支持发布管理**：`prepare`/`update`/`rollback` 需要与发布系统集成
2. **不支持备份**：必须使用宿主机脚本（PG dump、Redis RDB、卷快照）
3. **不支持流量管理**：Nginx 配置仍需通过宿主机或专用适配器
4. **无并发锁**：依赖上层 orchestrator 实现互斥
5. **固定 timeout**：120-180秒，不可运行时调整

### 未来计划

1. **发布集成**（高优先级）
   - 支持 prepare 阶段（隔离环境演练）
   - 实现 update 流程（固定 digest + 备份 + 迁移验证）
   - 提供 rollback 能力（回滚到已知良好版本）
   
2. **多资源编排**（中优先级）
   - 自动处理 Web + DB 的启动顺序
   - 卷独占保护（防止双 writer）
   - 跨服务的健康依赖检查
   
3. **observability 增强**（低优先级）
   - 记录 API 调用延迟
   - 导出 Prometheus 指标（状态变更、错误率）
   - 集成 Alertmanager 故障通知

## 安全考虑

### 威胁模型

1. **凭证泄露**：Token 文件被未授权用户读取
   - 缓解：强制 `0[0-7]00` 权限，root-only 目录
   - 检测：定期审计文件权限（audit.d 规则）
   
2. **日志泄露**：Token 出现在日志或错误消息中
   - 缓解：错误脱敏机制，清除 Bearer/Authorization 字样
   - 检测：日志扫描工具检查敏感模式
   
3. **SSRF 攻击**：恶意配置 coolifyEndpoint 指向内网服务
   - 缓解：Schema 验证（强制 http/https），UUID 白名单
   - 限制：仅允许已知 Coolify 实例地址
   
4. **重放攻击**：Token 被截获后重复使用
   - 限制：Coolify 本身不支持 token 过期/轮换
   - 建议：定期手动轮换 token（每季度）

### 审计日志

所有 Coolify API 调用应记录：

- 时间戳
- 操作人（principal）
- 服务名称和 UUID
- 动作类型（start/stop/restart）
- 结果（成功/失败/超时）

当前通过上层 orchestrator 的审计机制实现，适配器本身不直接写审计日志。

## 测试覆盖

### 单元测试

位置：`adapters/tests/test_coolify_service_adapter.py`

覆盖场景：
- ✅ Token 文件权限验证（拒绝 group/other 可读）
- ✅ Token 脱敏（错误消息中不泄露）
- ✅ API 超时强制执行（不无限挂起）
- ✅ HTTP 状态码处理（2xx 成功，其他失败）
- ✅ 依赖状态追踪（操作期间不变）
- ✅ 启动/停止/重启生命周期
- ✅ 健康检查等待（有界超时）
- ✅ 配置验证（UUID 格式、endpoint schema）
- ✅ 未实现动作明确拒绝（prepare/update/rollback）
- ✅ 备份动作拒绝（必须用宿主脚本）

运行测试：

```bash
cd /workspace/services/areasong-ops
python3 -m unittest adapters.tests.test_coolify_service_adapter -v
```

### 集成测试（待实现）

需要真实 Coolify 环境：

1. 创建测试服务（最小镜像如 nginx:alpine）
2. 执行完整生命周期（stop → start → restart）
3. 验证健康检查和状态等待
4. 测试失败恢复（API 错误、超时）

### 生产验收测试

在正式迁移前，必须完成：

- [ ] 只读操作（inspect）连续 24 小时无错误
- [ ] 重启操作在维护窗口成功完成（含健康检查）
- [ ] 依赖服务保护机制验证（手动触发依赖变化）
- [ ] Token 轮换流程测试（更新 token 文件并验证）
- [ ] 故障注入测试（模拟 Coolify API 错误、网络超时）
- [ ] 监控告警验证（操作失败触发通知）

## 相关文档

- Coolify 官方文档: https://coolify.io/docs
- Coolify API v1 文档: https://coolify.io/docs/api/v1
- AreaSong Ops 架构: `standards/04-deployment.md`
- 备份与恢复: `standards/06-backup-dr.md`
- 变更管理: `standards/05-change-management.md`
