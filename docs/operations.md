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
- Telegram `/new` 按聊天保存当前会话，重启后恢复。普通消息和 `/retry` 使用接收时绑定的会话。群成员可以用 `/persona` 查看预设，会话创建者可以切换。`/stop` 会请求取消该聊天的活跃生成，包含刚被 `/new` 切换掉的旧会话。取消后会尝试删除空占位消息。
- `/retry` 重用当前会话中该发送者最近一次获准生成的输入。失败后的输入仍可重试，过期尝试标识会被拒绝。成功的重试追加一组用户消息和回答，已有回答保留。失败或取消的生成不写入历史。
- Telegram 按 `(chat_id,message_id)` 缓存命令结果。Webhook 回执发送失败时，重推会复用缓存。成功的发送回执落库后，后续重推会跳过发送。若进程在 Telegram 接受回执与本地落库之间退出，或长回复只有部分分段送达，重推仍可能重复发送。`sessions.reset` 会删除重试输入并使缓存回答失效。该会话在生成或发送回执期间，重置会返回冲突。
- 长轮询使用有界后台任务，普通生成期间仍可处理 `/stop`。轮询确认位置保持在最早未完成的更新，当前轮询运行内会跳过重复收到的已处理更新。被关闭打断的更新仍可由 Telegram 重投。绑定会话或处理短命令出错时，每条更新在当次运行中最多尝试两次。聊天忙碌或任务已满时会尝试发出提示，发送者可以稍后重试。
- 加密备份包含 Telegram 会话指针、重试输入和命令状态，导入会在写入前校验 Telegram 记录。旧群聊历史在首次访问时迁移为按聊天归属，保留创建者和历史。白名单和目标 bot 校验发生在会话绑定之前。
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
