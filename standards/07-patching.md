# 07 补丁与漏洞管理

## 更新策略

| 类型 | 频率 | 方式 | 窗口 |
|------|------|------|------|
| 安全补丁 | 自动 + 月度确认 | unattended-upgrades / dnf-automatic | 自动安装 |
| 内核更新 | 季度 | 手动，需 reboot | 维护窗口 |
| 应用依赖 | 按需 | 手动升级 + 测试 | 维护窗口 |
| Docker 镜像 | 月度 | 重建镜像 + 滚动更新 | 维护窗口 |

## 自动安全更新

### Ubuntu/Debian

```bash
apt install unattended-upgrades
dpkg-reconfigure -plow unattended-upgrades
```

配置 `/etc/apt/apt.conf.d/50unattended-upgrades`：

- 仅安装安全更新（`-security` origin）
- 自动清理 unused 包
- 邮件通知（可选）

### CentOS/RHEL

```bash
dnf install dnf-automatic
systemctl enable --now dnf-automatic.timer
```

配置 `/etc/dnf/automatic.conf`：

```ini
apply_updates = yes
upgrade_type = security
```

## 手动补丁窗口

每月第一个周日 02:00-06:00（UTC）为补丁维护窗口：

1. 查看可用更新：`apt list --upgradable` 或 `dnf check-update`
2. 先在 test 环境验证
3. 生产环境逐台更新（不同时更新所有机器）
4. 更新后验证服务状态
5. 内核更新需 reboot，逐台进行

## 漏洞响应

| CVSS 评分 | 响应时间 | 动作 |
|-----------|----------|------|
| 9.0-10.0（Critical） | 24 小时内 | 紧急补丁或临时缓解 |
| 7.0-8.9（High） | 7 天内 | 下次维护窗口修复 |
| 4.0-6.9（Medium） | 30 天内 | 常规更新 |
| 0.1-3.9（Low） | 下季度 | 随常规更新 |

## 漏洞扫描（可选）

- 系统级：`apt audit`（Ubuntu）或 `dnf updateinfo`（RHEL）
- 容器镜像：Trivy 扫描（`trivy image <image>`）
- 建议每季度全量扫描一次，结果记入 Git

## 禁止

- 在生产环境直接 `apt upgrade` 不加确认
- 跳过 test 环境直接上生产
- 同时更新所有机器（留一台作对照）

---

修订记录：

- 2026-07-02 初版
