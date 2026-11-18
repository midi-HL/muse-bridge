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
| 文件生成物 | `/files/{id}` | 需管理会话或外部 API key；强制下载 |
| 账号导入/探活 | `accounts.import/list/probe` | 依赖 pool/core；失败不得伪造成功 |
| 节点、会话、定时任务、人设 | `nodes.*`、`sessions.*`、`schedules.*`、`personas.*` | 已接入 SQLite 持久化；节点 URL secret 加密，State 只返回掩码；节点测试通过固定 Muse session endpoint，计划任务按时区去重调度并写入日志/回调 |
| 贴纸、ASR | `stickers.*`、`asr.*` | 贴纸描述 CRUD、Telegram `t.me/addstickers` 元数据导入、ASR 测试/模型安全下载已接入；视觉描述和未配置 ASR 模型明确返回 unsupported/配置错误 |
| 加密备份 | `backup.export`、`backup.import` | AES-GCM + 独立口令；导入先校验，允许覆盖并支持失败回滚；禁止恢复管理员会话/API key |
| Hatch 任务/线程/协议探活 | `protocol.probe`、`protocol.tasks`、`protocol.threads` | `protocol.probe` 探测账号池并返回已确认能力；远端任务/线程/额度/取消仍明确 unknown/unsupported，不猜参数 |
| Telegram | `internal/channels/telegram`、`/telegram/webhook` | 已提供长轮询/webhook、白名单、命令分派、流式回调和运行时热更新；webhook 校验 `X-Telegram-Bot-Api-Secret-Token`，WebUI/CLI 保存 token、模式、secret 或白名单后立即重载，需部署机填写 Bot token 和公网 HTTPS webhook URL 才能进行外部验收 |

协议可达性不能由静态 action 名称推断；每项真实能力必须用本地安全 cookie 在部署机验证。CI、日志、报告和仓库均不含用户凭证。

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
