# 脚本目录

## 工具脚本

| 脚本 | 用途 |
|------|------|
| generate-ansible-inventory.py | 从 inventory/servers.yaml 生成 Ansible inventory |

## 备份脚本（规划中）

备份脚本将放在 `backup/` 子目录，由 cron 调度：

```
scripts/backup/
├── backup-mysql.sh
├── backup-redis.sh
├── backup-configs.sh
└── README.md
```

详见 [standards/06-backup-dr.md](../standards/06-backup-dr.md)。
