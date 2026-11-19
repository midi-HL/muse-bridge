# Muse Bridge / Muse 聊天桥

Go 单二进制服务，把 Muse 网页账号的 Hatch 协议连接到管理台、OpenAI 兼容 API 和 CLI。项目由辰星（midi-HL）维护，协议行为参照 `lichao199208/muse2api`；媒体、Telegram、贴纸与 ASR 的处理逻辑参考 `midi-HL/picoclaw-edition`，管理台界面为本项目独立设计。本项目不是 Muse 官方产品。

## 安全安装

只从可信的 GitHub Release 下载与 `SHA256SUMS` 匹配的产物。首次部署建议先只绑定 `127.0.0.1`，完成管理台设密后再通过受控反向代理暴露；非回环监听必须设置 `MUSE_SETUP_TOKEN`。不要把 cookie、管理密码、API key 写进仓库、命令历史、Issue、CI 日志或截图。

```powershell
$env:MUSE_MASTER_KEY = (openssl rand -base64 32).Trim()
$env:MUSE_SETUP_TOKEN = "use-a-long-random-one-time-setup-token"
.\muse-bridge-windows-amd64.exe serve
```

`MUSE_MASTER_KEY` 必须是 base64 编码的 32 字节密钥。主密钥丢失时，SQLite 中的加密 cookie、管理员秘密和 API key 无法恢复。数据目录默认是当前目录 `data`，文件权限按 0700/0600 创建。Windows 请把数据目录放在用户私有目录并限制 ACL。

## `telegram.bot_username` 可选，用于校验群聊中的 `/stop@bot`、`/new@bot` 等目标命令；未配置时带 `@目标` 的命令会被忽略，避免误执行给其他机器人发送的命令。长轮询会有界分派生成请求，使生成期间 `/stop` 仍可处理。

Telegram `/new` 的当前 chat 会话会持久化并在重启后恢复；私聊直接处理文本，群聊普通文本需明确 @ 当前 Bot 或回复当前 Bot，群聊命令仍可用。`/retry` 按发送者的 attempt ID 重试最近一次输入，生成失败仍可重试，相同文本不会覆盖之前成功回合。Webhook 重投同一 command 更新会复用缓存结果。`/sessions [页码]` 列出当前聊天的本地会话，`/switch ID` 切换，`/rename 名称` 由会话创建者改名，`/history` 查看最近消息；群聊切换影响整个聊天。

## 环境变量与配置

默认地址为 `127.0.0.1:8787`，`MUSE_ADDR` 覆盖监听地址。`MUSE_DATA_DIR` 覆盖数据目录，`MUSE_CONFIG` 指向 YAML 配置；`MUSE_SETUP_TOKEN` 控制首次远程设密；`MUSE_MASTER_KEY` 是必填主密钥。配置字段、默认值、范围和 secret 标记由 `/schema` 返回。普通设置可放 YAML 或 `MUSE_<GROUP>_<NAME>` 环境变量，secret 只从环境变量或加密存储读入；状态和日志只返回 `configured/masked`，不会返回明文。

建议的最小 YAML：

```yaml
general:
  timezone: Asia/Kuala_Lumpur
limits:
  concurrency: 2
  queue_size: 20
```

## 启动与 CLI

```text
muse-bridge serve [--addr 127.0.0.1:8787] [--data-dir data] [--config settings.yaml]
muse-bridge [--url URL] [--auth-file PRIVATE_JSON] status
muse-bridge config schema|get [KEY]
muse-bridge config set KEY --value JSON | --value-env ENV_NAME
muse-bridge accounts import --file PRIVATE_FILE | --env ENV_NAME
muse-bridge accounts list|probe [ID]
muse-bridge chat --prompt TEXT --model muse-spark [--persona ID|_none] [--stream=true]
muse-bridge chat --cookie env:NAME|file:PRIVATE_FILE --prompt TEXT
muse-bridge keys create --name NAME --out PRIVATE_FILE
muse-bridge keys revoke ID
muse-bridge backup export|import --file FILE --passphrase-env MUSE_BACKUP_PASSPHRASE
muse-bridge protocol probe
muse-bridge action NAME --args JSON | --args-file PRIVATE_FILE
```

CLI 服务地址默认是 `http://127.0.0.1:8787`；远程明文 HTTP 被拒绝，只允许 HTTPS。管理会话只从 `MUSE_ADMIN_COOKIE`、`MUSE_CSRF_TOKEN` 或 `MUSE_AUTH_FILE` 取得，外部 API 只从 `MUSE_API_KEY` 取得。`--cookie` 严格只接受 `env:NAME` 或 `file:PATH`，不接受 raw cookie；文件必须是私有文件。`keys create` 只把一次性 key 写入一个此前不存在的 0600 文件，终端只显示 ID 和保存结果。

## HTTP 接口

管理台优先注册 `/admin/api/`，其余 `/admin/` 路径由嵌入式 `web.Handler` 提供静态页面。

- `GET /admin/api/auth/status`、`POST /admin/api/auth/setup|login|logout`：HttpOnly、SameSite=Strict 会话 Cookie；写请求需要同源 `Origin` 与 `X-CSRF-Token`。登录失败按来源限速。
- `GET /schema`、`GET /state`、`POST /action`：统一 action registry，操作未知或未支持时返回 `501 unsupported`，危险操作由前端确认。
- `POST /chat`、`POST /admin/api/chat/stop`：管理会话 Web 聊天和 SSE。
- `GET /files/{id}`、`GET /admin/api/files/{id}`：管理员可读取生成物；外部 API key 只能读取该 key 生成的文件。缺少归属信息的旧文件仅管理员可读。文件通过生成物目录的受限句柄打开，拒绝越界路径与链接，响应强制附件下载。
- `GET /v1/models`、`POST /v1/chat/completions`：Bearer API key；支持 `muse-spark`、`muse-image`、`muse-video`、`gpt-4o-mini`，流式返回 OpenAI SSE，非流式返回 `chat.completion`。
- `POST /api/chat`、`POST /api/chat/stream`：简单同步/SSE 别名；API session 按 key 隔离。

错误只返回稳定的 code/message，不转发上游 body、URL、cookie 或 token。API key 与管理 console 会话永不互相替代。

## 账号路由与人设

账号支持 `node_id`、`pool`（`shared`/`chat`/`api`）与 `note`。开启 `pool.split` 后，Web/Telegram 使用 chat 或 shared 账号，外部 API/CLI 聊天使用 api 或 shared 账号；关闭后共用所有可用账号。绑定节点失效会拒绝使用该账号，不会自动绕过绑定直连。节点或 Cookie 更新在当前请求结束后生效；更新显示名称或备注不会打断生成。重复导入同 ID Cookie 保留未明确提供的账号配置与启停状态。

人设预设保存正文后，可在聊天页选择、CLI 使用 `--persona ID`，或 Telegram 使用 `/persona ID`。`_none` 表示关闭预设（Telegram 用 `/persona none`）；未指定时，新会话使用 `persona.default_id`，后续消息保留该会话的选择。OpenAI 兼容接口支持可选扩展字段 `persona_id`，管理 action 为 `sessions.persona`。人设只是引导，无法移除 Muse 自带系统提示词；本地自动化验证不代表真实 Muse 一定遵循人设。Telegram `/new` 会持久化当前 chat 的会话指针，`/retry` 会按发送者的 attempt ID 重试最近一次请求（包含首次生成失败的情况），失败不会污染历史；长轮询生成期间 `/stop` 仍可到达。

## 能力限制

协议、账号池、文件物化、设置、节点、人设、计划任务、会话和加密备份存储在 `internal/hatch`、`internal/pool`、`internal/core` 中；本层只负责 HTTP、CLI 和边界验证。节点连通性测试、Telegram 贴纸包自动导入和未配置 ASR 模型时会明确返回 `unsupported` 或配置错误，不会假成功。`tools`、MCP、本地 Agent、账号注册、验证码绕过不在项目范围。真实 Muse 账号测试必须在部署机本地进行，CI 不含任何凭证。

## CI 与发布

每次 push/PR 执行 `gofmt`、`go vet`、`go test`、`go test -race`、`govulncheck`（缺少时自动安装）和纯 Go 构建。推送 `v*` tag 后，Release workflow 构建：Windows amd64/386/arm64，Linux amd64/386/arm64/arm（GOARM=7），并生成 SHA-256 校验文件和 GitHub Release。

```powershell
gofmt -w cmd internal web
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./cmd/muse-bridge
```

## 许可与出处

本项目使用完整的 CC BY-NC-SA 4.0 国际公共许可文本，见 [`LICENSE`](LICENSE)。第三方出处、上游 MIT 许可声明和非官方产品声明见 [`NOTICE`](NOTICE)。部署说明与能力盘点模板位于 [`docs/`](docs/)。
