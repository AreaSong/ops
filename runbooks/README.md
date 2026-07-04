# 故障处置手册

> 真实故障复盘后持续补充。复盘使用 `postmortem-template.md`。

## 手册索引

| 编号 | 场景 | 文件 |
|------|------|------|
| RB-01 | 服务返回 5xx | [service-5xx.md](service-5xx.md) |
| RB-02 | 磁盘空间不足 | [disk-full.md](disk-full.md) |
| RB-03 | MySQL 慢查询 | [mysql-slow-query.md](mysql-slow-query.md) |
| RB-04 | 机器失联 | [host-unreachable.md](host-unreachable.md) |
| RB-05 | LosAngeles 跨机器恢复演练 | [losangeles-cross-machine-restore-drill.md](losangeles-cross-machine-restore-drill.md) |
| RB-06 | LosAngeles 当前运维状态快照 | [losangeles-current-status.md](losangeles-current-status.md) |

## 通用排障原则

1. **先止血再查根因**：回滚/重启/切流量
2. **只读优先**：先收集证据，再提变更方案
3. **一次一个变更**：执行后立即验证
4. **记录一切**：时间线、命令、输出，用于复盘

## 复盘流程

1. 故障结束后 24 小时内填写 `postmortem-template.md`
2. 识别"有监控就能更早发现"的缺口
3. 更新对应 runbook（如发现新排查路径）
4. git 提交复盘记录
