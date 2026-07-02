# Warp Profile 配置说明

> 在 Warp 中配置：**Settings > Agents > Profiles**

## 推荐 Profile

### Prod（生产环境，默认使用）

| 设置项 | 值 | 说明 |
|--------|-----|------|
| Executing commands | **Always ask** | 所有命令变更需人工批准 |
| Reading files | Agent decides | 读文件可自动执行 |
| Creating plans | Agent decides | 复杂变更先出计划 |
| Calling MCP servers | Always ask | 外部工具调用需确认 |

- 导入 `allowlist.txt` 中的正则到 **Command allowlist**
- 导入 `denylist.txt` 中的正则到 **Command denylist**
- denylist 优先级高于 allowlist 和 Autonomy 设置

### Test（测试环境）

| 设置项 | 值 | 说明 |
|--------|-----|------|
| Executing commands | **Agent decides** | 常见只读自动，变更仍询问 |
| Reading files | Always allow | |
| Creating plans | Agent decides | |
| Calling MCP servers | Agent decides | |

- 同样导入 denylist（红线不可放松）
- allowlist 可适当放宽（如 ansible-playbook --check）

## 配置步骤

1. 打开 **Settings > Agents > Profiles**
2. 复制现有 Profile，分别命名为 `Prod` 和 `Test`
3. 按上表设置 Autonomy 级别
4. 打开 `allowlist.txt`，逐行复制正则到 **Command allowlist**
5. 打开 `denylist.txt`，逐行复制正则到 **Command denylist**
6. 在 Warp 输入框左侧点击 Profile 图标，按环境切换

## 注意事项

- denylist 可被 `bash -c "..."`、管道、变量拼接绕过——终端侧只是第一道防线
- 服务器侧 sudo 白名单和云只读子账号是第二、第三道防线（见 `standards/03-security-access.md`）
- 新增命令类型时同步更新 allowlist/denylist 并 git 提交
