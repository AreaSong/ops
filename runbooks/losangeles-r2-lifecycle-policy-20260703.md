# LosAngeles R2 生命周期策略记录

日期：2026-07-03 12:10 BST
服务器：LosAngeles
Bucket：`losangeles-ops-backups`
规则来源：用户在 Cloudflare 控制台配置并截图确认

## 1. 结论

R2 生命周期保留策略已配置。

当前规则：

| 规则 | 前缀 | 操作 | 状态 |
| --- | --- | --- | --- |
| `losangeles-expire-after-90-days` | `losangeles/` | 90 天后删除对象 | 已启用 |
| `Default Multipart Abort Rule` | 无前缀 | 7 天后中止未完成分片上传 | 已启用，保留 |

## 2. 说明

- `losangeles-expire-after-90-days` 用于控制异地备份存储增长，保留 90 天恢复窗口。
- `Default Multipart Abort Rule` 只清理未完成的 multipart upload，不删除正常备份对象，保留有助于避免失败上传残片长期占用空间。
- 当前未启用“不频繁访问”存储类转换。

## 3. 未验证项

- 本次没有使用 Cloudflare API 拉取规则；结论基于用户控制台截图确认。
- 实际删除动作由 Cloudflare R2 生命周期机制异步执行，通常不是保存后立即删除到期对象。
