# 部署、能力与验证说明

## Windows 部署

1. 从 GitHub Release 下载 `muse-bridge-windows-amd64.exe` 和对应 `.sha256`，在 PowerShell 用 `Get-FileHash` 对照。
2. 创建仅当前用户可读的数据目录，并以环境变量注入 `MUSE_MASTER_KEY`；不要把密钥写入脚本仓库。
3. 首次启动保持 `MUSE_ADDR=127.0.0.1:8787`，浏览器打开 `http://127.0.0.1:8787/admin/` 设定密码。
4. 需要 Tailscale/反代访问时，改为显式受控监听并设置 `MUSE_SETUP_TOKEN`；优先使用 TLS 反代，限制来源网段。
5. 如需开机自启，使用当前用户计划任务调用启动脚本；接通电源时禁用自动睡眠，否则服务会停止。

停止服务发送 Ctrl+C。程序等待活动请求结束并关闭 Hatch 连接；SQLite WAL 和文件目录在同一数据目录。

## 客户端能力对照表（当前实现边界）

| Muse/管理能力 | HTTP/action 名称 | 当前证据与限制 |
|---|---|---|
| 设置、状态、日志 | `settings.update`、`/state` | core 持久化设置；secret 只返回 mask |
| API key | `keys.create/list/update/revoke` | key 仅创建响应一次；认证限速按 key |
| Web/OpenAI 聊天 | `/chat`、`/v1/chat/completions` | SSE、非流式和 stop 已接入 core |
| 文件生成物 | `/files/{id}`、`/admin/api/files/{id}` | 管理员可下载；API key 限于所属文件。旧的无归属记录仅管理员可读；生成物目录以 `os.Root` 限定，拒绝路径穿越与链接 |
| 账号导入/探活 | `accounts.import/list/probe` | 依赖 pool/core；失败不得伪造成功 |
| 节点、会话、定时任务、人设 | `nodes.*`、`sessions.*`、`schedules.*`、`personas.*` | 已接入 SQLite 持久化；节点 URL secret 加密，State 只返回掩码；节点测试通过固定 Muse session endpoint，计划任务按时区去重调度并写入日志/回调 |
| 贴纸、ASR | `stickers.*`、`asr.*` | 贴纸描述 CRUD、Telegram `t.me/addstickers` 元数据导入、ASR 测试/模型安全下载已接入；视觉描述和未配置 ASR 模型明确返回 unsupported/配置错误 |
| 加密备份 | `backup.export`、`backup.import` | AES-GCM + 独立口令；导入先校验，允许覆盖并支持失败回滚；禁止恢复管理员会话/API key |
| Hatch 任务/线程/协议探活 | `protocol.probe`、`protocol.tasks`、`protocol.threads` | `protocol.probe` 探测账号池并返回已确认能力；远端任务/线程/额度/取消仍明确 unknown/unsupported，不猜参数 |
| Telegram | `internal/channels/telegram`、`/telegram/webhook` | 已提供长轮询/webhook、白名单、命令分派、目标 bot 校验、流式回调和运行时热更新；webhook 校验 `X-Telegram-Bot-Api-Secret-Token`，WebUI/CLI 保存 token、模式、secret 或白名单后立即重载，需部署机填写 Bot token 和公网 HTTPS webhook URL 才能进行外部验收 |

协议可达性不能由静态 action 名称推断；每项真实能力必须用本地安全 cookie 在部署机验证。CI、日志、报告和仓库均不含用户凭证。

生成物备份仅包含记录，不包含媒体字节。新文件使用生成物目录相对名称；迁移时须另行复制 `files/` 内容。旧版指向当前生成物目录的路径仍兼容；迁移到新目录时，符合旧 `files/<文件ID>-<文件名>` 布局的记录仅取文件名并重定位到当前生成物目录，绝不读取原机器路径。其他目录外路径、路径穿越、符号链接和非法记录会在任何备份写入前被拒绝。

`asr.download` 可提交可选的 64 位十六进制 `sha256`，不匹配时不写文件；响应的 `path` 为相对数据目录的模型位置，不含宿主机绝对路径。下载限制按实际解码后字节数执行（上限 128 MiB），DNS、连接和读响应共用最多 60 秒期限；禁止自动代理和重定向，私网目标会被拒绝。

`telegram.bot_username` 为可选配置，填入当前 Bot 的 username（可带或不带 `@`）；只有目标匹配本 Bot 的 `/command@bot` 才执行。未配置时所有带目标的命令都会忽略，无目标的普通 `/command` 不受影响。

## 账号节点、流量分池和人设

- 账号 `node_id` 绑定启用节点并读取其加密代理地址，下一次请求会检查节点是否仍有效、URL 是否更新。删除/禁用绑定节点后，该账号不可使用；清空 `node_id` 才恢复默认代理配置。没有实现自动选优或故障切换到其他节点。
- `pool.split=true` 时 API 使用 api/shared，Web/Telegram 使用 chat/shared；无符合条件的账号立即失败，只有账号忙碌时才等待。旧账号未设置 pool 按 shared 处理，刷新 Cookie 不重置 pool/node/note/enabled。JSON 导入可以显式提供这些字段。
- 节点或 Cookie 更新等当前请求完成后用于下一次请求；修改 label/note 不关闭当前连接。删除账号或停止服务仍会关闭连接。请求完成不得覆盖期间写入的新账号元数据或凭证。
- 新 Web 会话未选择人设时使用 `persona.default_id`；选择后随会话持久化。缺失或停用的人设在调用生成前报错，不写入失败用户消息。生成中的同一会话不接受第二个生成或人设/历史改写。
- CLI：`chat --persona ID` 或 `--persona _none`；临时 `--cookie` 模式没有服务端预设库，会拒绝同时指定保存的人设。OpenAI 请求可添加 `persona_id`；管理员 action `sessions.persona` 使用 `id` 和 `persona_id`。Telegram 使用 `/persona` 查看、`/persona ID` 切换、`/persona none` 关闭。
- Telegram `/new` 会为当前 chat 持久化新的会话指针；聊天、`/stop`、`/persona` 和 `/retry` 使用该指针，重启后恢复。`/retry` 重用该发送者最近一次通过 core 会话租约的尝试，并携带不可复用的 attempt ID；新请求会使排队中的旧 retry 失效。首次生成失败也可重试；每次 retry 是新尝试、追加独立 turn，不会按相同文本覆写其他成功答复；失败不污染历史。
- retry 记录按 sender ID 隔离。同一 Webhook command 的 `(chat_id,message_id)` 结果持久化，重复投递复用缓存结果、不重复生成。`sessions.reset` 会清除 retry 记录与缓存 command answer；命令完成写回与 reset 共享互斥区，不能用旧结果覆盖失效标记；与正在执行的 core chat 冲突时拒绝 reset。
- Telegram pointer、retry 和 command 状态纳入加密备份导入/导出。旧版本 `telegram_<chatID>` 群聊历史首次访问时保留旧创建者标识，并将历史 owner 迁移为 chat owner，避免升级后丢失上下文。Webhook 和长轮询均在白名单/目标预检后、发送状态占位或提交异步队列前绑定会话，避免 `/new` 竞态；普通重试按 attempt ID 验证，重复 webhook command 回执按 `(chat_id,message_id)` 幂等。
- 长轮询对普通生成采用有界后台分派；每条更新在排队/发送占位消息前绑定会话指针，后续 `/new` 不会把已接收消息改投到新会话。忙碌或队列满时回复明确提示、要求发送者重试；控制命令独立处理，`/stop` 会取消该 chat 所有已注册 core lease；生成取消会清理未显示内容的 Telegram 占位消息。更新写入服务前遵循 polling offset 处理，外部 E2E 未运行。
- 人设是尽力引导，不替换 Muse 内置提示词。前端测试覆盖选择、失败恢复、异步切会话和定时刷新竞态；Telegram session/retry 由 core 与 command 测试覆盖；尚未完成真实浏览器视觉走查或真实 Muse 人设服从测试。

## 验证记录模板

```text
go vet ./...
gofmt -l cmd internal web
go test ./...
go test -race ./...
govulncheck ./...
CGO_ENABLED=0 go build ./cmd/muse-bridge
# Race testing requires a host C compiler (Ubuntu runners provide gcc).
CGO_ENABLED=1 go test -race ./...
```

真实部署测试应记录首次设密、登录/登出、CSRF/Origin 拒绝、API key 与管理 cookie 隔离、OpenAI 流式/非流式、文件下载、重启持久化及失败 cookie 的明确错误。上游 Muse 连接、Telegram、媒体、ASR 需在目标机单独测试，不能以模拟 core 通过替代。
