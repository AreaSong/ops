# Clash 直连规则管理器

给本机 Clash Verge 的 **`sub`** 订阅添加、查看和删除临时直连例外。独立于 Codex 运行，不需要每次让助手改配置。

“临时”指手动添加、用完删除，没有自动到期。工具只保存源脚本，**不主动重载、不切换订阅、不改 DNS/TUN/节点、不操作服务器**。

## 最简用法

1. 在 Finder 中双击 [Clash直连规则.command](Clash直连规则.command)。也可以在终端执行本目录的 `./clash-direct`。
2. 选 **1 添加直连**，粘贴网址。默认只匹配完整域名；需要包含子域名时选择 2。查看预览后输入 `y` 保存。
3. 去 Clash Verge 的 **订阅 → sub → 重新激活订阅**，应用新规则。不要点“更新所有订阅”。

删除时选 **3 删除条目**，输入列表序号（可用 `1,3` 同时删除多项），确认保存，再手动重载。误删可选 **4 撤销上次修改**，仍需重载。

本目录可以整体移动。想放桌面时，建议给 `.command` 创建替身，不要只把这个文件单独移走。

## 匹配范围与注意事项

- 输入 `https://docs.example.com:8443/page?token=...`，仅保存 `docs.example.com`，不保存路径、查询参数和片段；拒绝带用户名/密码的网址。
- 带敏感参数的链接优先在交互菜单中粘贴；命令行参数本身可能进入终端历史，本工具无法清理终端历史。
- `exact` 生成 `DOMAIN,docs.example.com,DIRECT`，对该主机的全部路径、端口和协议生效。
- `suffix` 生成 `DOMAIN-SUFFIX,docs.example.com,DIRECT`，包括该域名及子域名，**不会自动去掉 `www` 或扩大到上级域名**。
- 支持标准域名、国际化域名（按浏览器的 IDNA 规则转为 ASCII）、单个 IPv4/IPv6。IP 使用 `/32` 或 `/128` 加 `no-resolve`，不支持批量 CIDR 网段。
- 顶级域名和常见公共后缀禁止使用 `suffix`。这不是完整公共后缀库，仍应检查预览中的覆盖范围。
- 自定义例外优先于原分流和拦截规则；不要给不信任的网站添加过宽的例外。重定向到另一域名时，另一域名仍按原规则处理。
- **DIRECT 不等于绕过 TUN，也不等于 DNS 改为直连。** 原有 DNS 策略保持不变；如果解析仍依赖代理节点，节点故障时新增 DIRECT 也不一定能恢复访问。
- 删除只移除本工具条目，恢复原分流。学校等原有直连规则或另一个上级域名例外仍可能使网站直连。
- 保存不代表已经生效。工具不触发重载，但 Clash 自身的订阅自动更新也可能重新应用已保存的扩展。

## 接入与保护方式

首次确认保存时，工具把原脚本的 `main` 改名为 `__clash_direct_original_main__`，保留其主体，在文件末尾加入带版本和订阅标识的管理区。新 `main` 先运行原逻辑，再把你的直连清单置顶，避免现有外网优化把临时 DIRECT 覆盖掉。

后续只更新自己的管理区，原脚本主体、节点和 DNS 不改。清单为空时，管理层不会新增分流规则。不要手工编辑管理区；代码或标记被修改时工具会拒绝覆盖。

工具要求唯一的 `sub`、独立的本地脚本和当前用户所有的普通文件。`sub` 未启用、脚本被共享/链接、入口不受支持、预览后文件变化或另一实例正在保存时，会停止写入。复杂或递归 `main` 不是支持的自动接入形式。

每次保存前运行 Node **语法检查（不执行现有脚本）** 和 Mihomo `-t` **合成规则检查**。这些离线检查不代表实际内核已应用新配置。

备份、撤销记录和锁放在本目录的 `.state/<订阅标识>/`：目录权限 700，文件权限 600，已被 Git 忽略。备份含当时的脚本内容，**不要分享或提交 `.state`**。撤销只恢复本工具清单，不撤销你对原脚本主体的其他修改。

不要同时在 Clash 编辑器和工具里保存同一脚本。工具提供锁与哈希冲突检查，但不能与所有外部编辑器建立全局事务。写入状态不明或保存后索引失败时不会自动重试；先保留错误提示和备份，再检查。

## 命令行

以下命令在本目录运行；从其他目录调用时使用 `clash-direct` 的绝对路径。没有安装全局 PATH 命令或 Codex 伴随技能。

```sh
./clash-direct --help
./clash-direct --json doctor
./clash-direct --json target
./clash-direct resolve 'https://docs.example.com/page' --json
./clash-direct list --json
./clash-direct add 'https://docs.example.com/page' --dry-run --json
./clash-direct add docs.example.com
./clash-direct add example.com --scope suffix
./clash-direct remove docs.example.com
./clash-direct undo
./clash-direct export
```

`add/remove/undo` 默认预览后询问。非交互或 `--json` 模式必须先查看 `--dry-run`，再明确使用 `--yes` 才能保存。只读命令不会接入管理层、建立持久备份或重载。

`export` 是只读原始规则出口，仅导出本工具的规则，不导出节点、订阅链接或原脚本。

高级路径参数：`--app-dir`（用于隔离测试或自定义 Clash 数据目录）、`--state-dir`（独立私有备份目录）、`--node`、`--core`。默认目标仍固定为 `sub`，不会自动改其他订阅。

## 依赖与检查

本机已验证：macOS、Python 3.9+、PyYAML 6、Node、Clash Verge 自带 Mihomo。脚本不自动安装任何依赖。`.command` 使用本目录 `.venv/bin/python`（若存在），否则使用 `/usr/bin/python3`。

如以后更换 Python 环境，可自行在本目录创建虚拟环境，再按 [requirements.txt](requirements.txt) 安装依赖；无需全局安装。Node 和 Mihomo 缺失时会提示，不会跳过必要校验直接保存。

```sh
python3 -m unittest discover -s tests -v
./clash-direct --json doctor
```

测试写入仅发生在隔离的临时夹具中；`doctor` 和 `--dry-run` 只读取真实目标，校验文件在自动清理的临时目录中生成。

## JSON 与退出码

成功统一为 `{"ok":true,"command":"list","data":{...}}`。例如 `resolve` 的 `data` 包含 `host/scope/kind/rule/warnings/saved:false`；`list` 包含 `target/rules/source_layer_present/runtime_verified:false`；`export --json` 的 `rules` 是原始规则字符串列表。

预演成功返回 `data.preview` 和 `saved:false`。保存成功返回 `saved:true`、`backup`、`reload_required:true`、`reload_triggered:false`；它不声称运行态已生效。

错误统一为 `{"ok":false,"command":"add","error":{"code":"confirmation_required","message":"...","details":{...}}}`。错误不回显完整网址、订阅链接或原脚本源码。

`doctor` 的 `ok:true` 只表示检查已执行，**必须看 `data.ready` 和退出码**；它也报告 `mode:local_offline`、`auth:not_required`，不联网验证账号或网站。

退出码：0 成功/无变化；2 输入、依赖、配置或确认不满足；3 冲突；4 校验失败；5 写入失败/部分保存/状态不明；130 中断。收到 5 时先检查，不要盲目重试。`--help` 始终输出人类可读帮助。
