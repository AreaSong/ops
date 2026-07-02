# RB-04 机器失联

## 症状

- SSH 连接超时或拒绝
- 监控告警：HostDown (up == 0)
- 云控制台显示实例状态异常

## 快速止血

```bash
# 1. 确认是网络问题还是机器问题
ping -c 3 <private_ip>
ping -c 3 <public_ip>    # 如有公网 IP

# 2. 检查云控制台实例状态
aliyun ecs DescribeInstances --InstanceIds '["<instance-id>"]'
# 或
tccli cvm DescribeInstances --InstanceIds '["<instance-id>"]'

# 3. 如果是云实例异常，通过控制台 VNC 登录排查
# 4. 如果完全无法恢复，准备故障转移方案（切换 DNS/负载均衡到备用机器）
```

## 排查步骤

### 1. 网络层排查

```bash
# 从其他正常机器 ping/traceroute
ping -c 5 <target_ip>
traceroute <target_ip>

# 检查安全组/防火墙是否误改
aliyun ecs DescribeSecurityGroupAttribute --SecurityGroupId <sg-id>
```

### 2. SSH 层排查

```bash
# 详细 SSH 连接信息
ssh -vvv user@<target_ip>

# 常见错误：
# - Connection refused → sshd 未运行或端口不对
# - Connection timed out → 网络/防火墙/安全组问题
# - Permission denied → 密钥/账号问题（机器其实在线）
```

### 3. 通过云控制台 VNC 登录

如果 SSH 不通但实例显示 Running：

1. 云控制台 → 实例 → 远程连接 → VNC
2. 登录后检查：

```bash
# 系统是否响应
uptime
df -h
free -h

# sshd 状态
systemctl status sshd

# 网络接口
ip addr
ss -tlnp | grep 22

# 防火墙
ufw status          # Ubuntu
firewall-cmd --list-all  # RHEL

# 系统日志
journalctl -xe --no-pager | tail -50
dmesg | tail -30
```

### 4. 常见原因

| 原因 | 特征 | 处理 |
|------|------|------|
| OOM Kill | dmesg 有 "Out of memory" | 重启实例，排查内存泄漏 |
| 磁盘满 | df 100%，系统无法写 | 见 RB-02 |
| sshd 崩溃 | systemctl status sshd 失败 | systemctl restart sshd |
| 防火墙误改 | ufw/firewalld 阻止 22 | 通过 VNC 修复规则 |
| 安全组误改 | 云控制台安全组无 22 规则 | 恢复安全组 |
| 内核 panic | dmesg 有 "Kernel panic" | 重启实例，检查硬件/内核 |
| 网络配置错误 | ip addr 无正确 IP | 通过 VNC 修复网络配置 |

### 5. 实例无法恢复

如果实例完全无法恢复：

1. 确认最新备份可用（见 06-backup-dr.md）
2. 在新实例上恢复服务和数据
3. 更新 inventory/servers.yaml 和 DNS/负载均衡
4. 验证业务恢复

## 恢复验证

- [ ] SSH 可正常登录
- [ ] 监控 up == 1
- [ ] 所有服务正常运行
- [ ] 业务功能正常

## 后续

- 填写 postmortem-template.md
- 确认 HostDown 告警是否及时触发
- 检查是否有自动恢复机制（systemd Restart、Compose restart policy）
- 考虑是否需要备用实例
