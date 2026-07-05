# LosAngeles standards/09 批次 B3：fstab UUID 收敛

更新时间：2026-07-06  
服务器：LosAngeles  
范围：`/etc/fstab` 静态挂载标识从 `LABEL=` 切换为 `UUID=`  
风险级别：中；涉及启动链路配置，已验证当前运行态挂载，不在本批次重启

## 1. 本批次完成项

已将 `/etc/fstab` 中三处静态文件系统从 `LABEL=` 改为 `UUID=`：

| 挂载点 | 设备 | UUID |
| --- | --- | --- |
| `/` | `/dev/sda1` | `b2242f09-1910-41d3-9c09-52088fee4c4c` |
| `/boot` | `/dev/sda16` | `1ebfbd42-1ee8-422c-bd2d-bdcc4d0141a5` |
| `/boot/efi` | `/dev/sda15` | `C6BA-7341` |

保留不变：

- `/swap.img none swap sw,comment=cloudconfig 0 0`

## 2. 验证结果

已完成：

- `findmnt --verify --verbose --tab-file <candidate>`：`0 parse errors, 0 errors`
- 安装新 `/etc/fstab`
- `findmnt --verify --verbose`：`0 parse errors, 0 errors`
- `mount -a`：通过，无错误输出
- `systemctl daemon-reload`
- 复核当前挂载：
  - `/` 仍挂载自 `/dev/sda1`
  - `/boot` 仍挂载自 `/dev/sda16`
  - `/boot/efi` 仍挂载自 `/dev/sda15`

说明：

- `findmnt --verify` 对 `/swap.img` 给出常规 warning：`non-bind mount source /swap.img is a directory or regular file`。这是 swap 文件的正常形态，不是本次变更错误。
- 本批次不主动重启。启动链路最终验证留到下次维护窗口或自然重启后观察。

## 3. 备份与回滚

变更前备份目录：

- `/root/ops-change-backups/standards09-fstab-uuid-20260705160144`

关键备份文件：

- `fstab.before`
- `fstab.after.candidate`

如需回滚：

```bash
sudo cp /root/ops-change-backups/standards09-fstab-uuid-20260705160144/fstab.before /etc/fstab
sudo findmnt --verify --verbose
sudo mount -a
sudo systemctl daemon-reload
```

## 4. 验收结论

`fstab` UUID 收敛已完成。

当前仍未做的是重启级启动链路验证；不影响本次运行态验收，但建议在下一次计划维护窗口或自然重启后记录一次复核结果。
