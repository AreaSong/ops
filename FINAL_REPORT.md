# Coolify 适配器最终报告

## PR 信息

- **PR**: https://github.com/AreaSong/ops/pull/1416
- **分支**: `cursor/coolify-adapter-trusted-6445`
- **提交**: aeb07ea（初版）→ 5875ff5（安全修正）
- **状态**: 待审阅，已修正首版安全问题

## 实际修正内容

### 安全漏洞修正

| 问题 | 首版缺陷 | 修正措施 | 验证方式 |
|------|---------|---------|----------|
| **SSRF** | 只检查 `^https?://` 前缀，接受任意 URL | endpoint 必须在受信 root 配置预定义，严格 URL 解析（拒绝 userinfo/@） | 单元测试 `test_ssrf_protection_userinfo_rejected` |
| **凭证路径绕过** | tokenFile 来自服务配置，可读任意文件 | tokenFile 由受信配置指定，服务只能引用 trustedEndpointId | 架构变更 + 单元测试 |
| **UUID 未验证** | 接受任意 24 字符 UUID | UUID 必须在 allowedServiceUuids 白名单 | 单元测试 `test_uuid_must_be_in_allowlist` |
| **Token 属主** | 未检查文件属主 | 强制 root（uid 0），拒绝非 root | 单元测试 `test_token_file_owner_must_be_root` |
| **错误泄露** | 未限制上游 body 长度 | 截断到 500 字符，清除敏感头 | 单元测试 `test_large_error_body_is_truncated` |
| **latest 未传递** | 只验证不执行 | restart API 传递 `{latest: false/true}` | 代码审查 + 集成测试需要真实 Coolify |

### 配置架构变更

**原设计（首版）**：
```json
{
  "services": {
    "x": {
      "runtime": {
        "coolifyEndpoint": "https://user-specified-url",  // SSRF!
        "tokenFile": "/user/specified/path"              // 任意文件读取!
      }
    }
  }
}
```

**修正后（受信配置）**：
```
/etc/areasong-ops/coolify-trusted-endpoints.json (root-only)
  ├── trustedEndpoints[id].endpoint (预定义 URL)
  ├── trustedEndpoints[id].tokenFile (预定义 token 路径)
  ├── trustedEndpoints[id].allowedServices (service 白名单)
  └── allowedServiceUuids[uuid] = service (UUID 白名单)

/etc/areasong-ops/services.json
  └── services[x].runtime.trustedEndpointId: "id"  // 只能引用
```

服务配置**不能**指定 endpoint 或 tokenFile，只能引用预定义 ID。

## 测试结果

### 单元测试（19/19 通过）

```bash
cd services/areasong-ops
python3 -m unittest adapters.tests.test_coolify_service_adapter -v

Ran 19 tests in 182.188s
OK
```

**测试覆盖**：
- ✅ SSRF 防护（userinfo、endpoint allowlist、UUID allowlist）
- ✅ Token 安全（root 属主、0[0-7]00 权限、符号链接）
- ✅ 错误脱敏（Bearer/Authorization 清除、大 body 截断）
- ✅ 认证失败（401 明确报错、不重试）
- ✅ 生命周期（start、stop、restart、健康检查）
- ✅ 依赖追踪（快照对比、变化检测）
- ✅ 超时控制（API 超时、状态等待超时）
- ✅ 配置验证（UUID 格式、endpoint schema、未支持动作）

### 回归测试（7/7 通过）

```bash
cd services/areasong-ops
python3 -m unittest adapters.tests.test_compose_service_adapter -v

Ran 7 tests in 0.613s
OK
```

现有 compose-service 适配器不受影响。

### 未测试范围

- ❌ **真实 Coolify 集成**：所有测试都是 mock curl，未连接真实 Coolify 实例
- ❌ **latest 参数实际效果**：需 Coolify 4.7.3 验证 `{latest: false}` 行为
- ❌ **多资源编排**：未测试 Web+PG 启动顺序、失败恢复
- ❌ **同卷 writer 排他**：未实现旧容器检测
- ❌ **恢复预算验证**：未模拟 180s 总预算内的故障恢复
- ❌ **Runner 契约层集成**：未测试适配器与 Runner/schema4 协议的真实交互

## 仍缺功能（诚实报告）

### 阻塞生产上线

1. **同卷 writer 排他保护**
   - 现状：假设旧容器已停止（`restart=no`）
   - 风险：如果旧容器仍运行，双 writer 破坏数据（resume-jadeai SQLite、account-vault PG）
   - 需要：检测旧容器，拒绝启动或强制停止旧容器

2. **多资源编排**
   - 现状：只支持单资源操作（一次只管理一个 service）
   - 风险：account-vault 需先启 PG 后启 Web，手动操作易出错
   - 需要：自动化启动顺序 或 上层 orchestrator 接口

3. **恢复预算验证**
   - 现状：每个 API/resource 分别 120-180s，未验证总预算
   - 风险：多步骤操作超过 180s 共享预算
   - 需要：模拟故障场景，测试从 Web 不可用到恢复的总时长

4. **Runner 集成测试**
   - 现状：未测试适配器与 Runner/action 传参、返回值、schema4 验证
   - 风险：上线后发现协议不兼容
   - 需要：至少一个 mock Coolify + 真实 Runner 的契约测试

### 功能限制（不阻塞上线）

- ❌ **backup/restore**：明确拒绝，需宿主级脚本
- ❌ **prepare/update/rollback**：明确拒绝，需签名发布链集成
- ❌ **流量管理**：明确拒绝，Nginx 由宿主机管理
- ❌ **sub2api 迁移**：不可切换（需实现 prepare）

### 明确约束

1. **依赖追踪 ≠ 启动顺序编排**
   - 实现：操作前后快照对比（状态一致性检查）
   - 不是：Web 启动前自动先启 PG

2. **不能声称"可安全注册 4 项服务"**
   - ✅ areasong-ops、resume-jadeai：单资源，可独立管理（需确认旧容器已停）
   - ⚠️ account-vault（Web+PG）：多资源，需人工顺序操作或等待编排实现

3. **areaforge、sub2api 保留原适配器**
   - areaforge：签名发布链、update-agent timer 不兼容
   - sub2api：需先实现 prepare 动作

## 安全上线清单

### 代码层面 ✅

- [x] 安全漏洞修正
- [x] 19/19 单元测试通过
- [x] 7/7 回归测试通过
- [x] 受信配置架构
- [x] 错误脱敏

### 测试层面 ⏳

- [ ] 真实 Coolify 集成测试（使用测试实例）
- [ ] Runner 契约层集成测试（mock Coolify + 真实 Runner）
- [ ] 同卷 writer 排他保护测试
- [ ] 多资源编排测试（Web+PG 顺序、失败恢复）
- [ ] 恢复预算验证（180s 总预算模拟）

### 生产操作 ⏳

- [ ] 创建 `/etc/areasong-ops/coolify-trusted-endpoints.json`（root 属主 0644）
- [ ] 创建 `/etc/areasong-ops/secrets/coolify.token`（root 属主 0400）
- [ ] 验证旧容器已停止且 `restart=no`（`docker inspect`）
- [ ] 更新 `/etc/areasong-ops/services.json`
- [ ] 测试单资源 restart（areasong-ops 或 resume-jadeai）
- [ ] 配置监控告警（操作失败通知）

## 与首版对比

### 首版问题（aeb07ea）

- ❌ SSRF 漏洞：接受任意 URL
- ❌ Token 路径可被服务配置覆盖
- ❌ UUID 未验证白名单
- ❌ Token 未检查属主
- ❌ 错误可能泄露大 body
- ❌ latest 参数未传递
- ⚠️ 声称"可安全注册 4 项"（过度承诺）

### 修正后（5875ff5）

- ✅ SSRF 防护：受信 endpoint allowlist
- ✅ Token 路径由受信配置管理
- ✅ UUID 白名单验证
- ✅ Token 检查 root 属主
- ✅ 错误截断 500 字符
- ✅ latest 参数传递（需 Coolify 验证）
- ✅ 诚实报告能力边界：
  - 单资源可管理（需确认旧容器已停）
  - 多资源需编排（当前不支持）
  - areaforge/sub2api 保留原适配器

## 建议上线步骤

### 阶段 1：补充测试（估计 2-4 小时）

1. 创建测试 Coolify 实例（Docker 本地运行）
2. 实现 Runner 契约层测试（mock Coolify + 真实 Runner 协议）
3. 测试同卷 writer 检测逻辑（或实现明确拒绝）
4. 模拟恢复预算场景（故障注入）

### 阶段 2：单资源服务试点（维护窗口）

1. 部署配置文件（trusted-endpoints.json、services.json）
2. 测试 areasong-ops 单资源 restart
3. 观察 24 小时稳定性
4. 配置监控告警

### 阶段 3：多资源服务（需实现编排）

1. 实现 Web+PG 启动顺序编排
2. 或提供上层 orchestrator 接口
3. 测试 account-vault 整栈操作
4. 更新文档和 runbook

### 阶段 4：发布管理集成（长期）

1. 实现 prepare 动作（隔离环境演练）
2. 实现 update 流程（签名发布链）
3. 实现 rollback 能力
4. sub2api 迁移

## 技术债务

### 高优先级（阻塞上线）

1. **同卷 writer 排他**：实现或提供验证脚本
2. **多资源编排**：实现或提供 orchestrator 接口
3. **恢复预算验证**：模拟测试 180s 总预算
4. **Runner 集成测试**：真实协议验证

### 中优先级（增强）

1. **真实 Coolify 测试**：搭建测试实例
2. **监控集成**：Prometheus 指标、Grafana 仪表盘
3. **SSRF endpoint 白名单**：配置允许的 Coolify 实例地址

### 低优先级（长期）

1. **发布管理**：prepare、update、rollback
2. **sub2api 迁移**：Web 托管 + PG/Redis 原栈
3. **统一适配器框架**：抽取公共逻辑

## 总结

### 已完成 ✅

- 基础生命周期适配（inspect、start、stop、restart）
- 安全架构修正（受信配置、SSRF 防护、token 验证）
- 单元测试全覆盖（19/19）
- 回归测试通过（7/7）
- 诚实的能力边界说明

### 未完成 ❌

- 真实 Coolify 集成测试
- Runner 契约层集成测试
- 同卷 writer 排他保护
- 多资源编排
- 恢复预算验证
- 发布管理集成

### 风险评估

| 风险 | 等级 | 缓解 |
|------|------|------|
| SSRF | 低 | ✅ 受信配置 + 单元测试 |
| Token 泄露 | 中 | ✅ root-only + 脱敏 |
| 双 writer 破坏数据 | 高 | ⚠️ 需实现检测或人工确认 |
| 多资源编排错误 | 高 | ⚠️ 当前不支持，需上层编排 |
| API 超时影响可用性 | 低 | ✅ 有界超时 + fail-fast |
| 未测试真实环境 | 中 | ⚠️ 需补充集成测试 |

### 推荐决策

**不建议立即上线生产**，原因：
1. 未测试真实 Coolify 环境（所有测试都是 mock）
2. 缺少同卷 writer 排他保护（高风险）
3. 多资源编排未实现（account-vault 不可用）
4. Runner 契约层未验证（可能协议不兼容）

**建议步骤**：
1. 补充集成测试（真实 Coolify + Runner）
2. 实现或文档化同卷 writer 检测
3. 单资源服务试点（areasong-ops）
4. 多资源服务等待编排实现

---

**最终状态**: 首阶段安全生命周期实现完成，但需补充集成测试和关键保护机制后才可上线生产。

**PR**: https://github.com/AreaSong/ops/pull/1416
