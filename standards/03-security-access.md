# 03 安全与访问控制

> 纵深防御：Warp denylist → sudo 白名单 → 云只读子账号。

## 账号体系

| 账号类型 | 用途 | 权限 |
|----------|------|------|
| 个人账号 | 日常 SSH 登录 | sudo 白名单 |
| 服务账号 | 运行应用进程 | 无登录、无 sudo |
| 只读账号 | 排障查询 | 数据库 SELECT、云 Describe |
| 变更账号 | 紧急变更 | 完整 sudo（需 MFA/二次确认） |

## SSH 密钥管理

- 每人一把密钥，禁止共用
- 密钥长度 ≥ 4096（RSA）或 Ed25519
- 离职/轮换：从 `~/.ssh/authorized_keys` 移除旧密钥
- 禁止私钥入库、禁止私钥上传到服务器

## sudo 白名单

日常排障账号使用精确 sudo 白名单（`/etc/sudoers.d/ops-readonly`）：

```sudoers
# 只读排障
ops-user ALL=(ALL) NOPASSWD: /bin/systemctl status *, /bin/systemctl is-active *, /bin/journalctl *
ops-user ALL=(ALL) NOPASSWD: /usr/bin/docker ps, /usr/bin/docker logs *
ops-user ALL=(ALL) NOPASSWD: /usr/sbin/nginx -t
```

变更操作（restart、包安装、配置修改）需切换到变更账号或输入 sudo 密码。

## 服务运行账号

- 每个服务用专属低权限系统账号（mysql、www-data、redis 等）
- 禁止 root 跑服务
- Docker 容器内同样使用非 root 用户（USER 指令）

## 数据库账号

| 账号 | 权限 | 用途 |
|------|------|------|
| app_rw | SELECT/INSERT/UPDATE/DELETE（限业务库） | 应用连接 |
| ops_ro | SELECT（全库） | 排障只读 |
| ops_admin | ALL（限 localhost） | 变更操作 |

- 排障默认使用 ops_ro
- UPDATE/DELETE 必须带 WHERE，先用 SELECT 验证影响行数

## 云子账号划分

### 阿里云 RAM

| 子账号 | 权限策略 | 用途 |
|--------|----------|------|
| ops-readonly | 只读（Describe/List/Get） | 日常查询、Warp Agent |
| ops-operator | 运维变更（Create/Modify，不含 Delete/Release） | 资源变更 |
| ops-admin | 完整权限 | 紧急操作，需 MFA |

### 腾讯云 CAM

同上结构，对应策略：QcloudReadOnlyAccess、自定义运维策略。

**Warp Agent 默认绑定 ops-readonly 子账号的 AccessKey**，变更操作切换 ops-operator。

## 凭证管理

- 所有凭证存 `/opt/ops/secrets.env`（不入库）
- 云 CLI 配置：`~/.aliyun/config.json`、`~/.tccli/` 分 profile 管理
- 提交前检查：`git diff --cached` 不含凭证字符串

## 审计

- 系统命令审计：auditd 记录 sudo 操作（基线剧本可选启用）
- 变更审计：Git 提交记录 + Warp 批准记录
- 云操作审计：阿里云 ActionTrail / 腾讯云 CloudAudit（建议开启）

---

修订记录：

- 2026-07-02 初版
