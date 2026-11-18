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
muse-bridge chat --prompt TEXT --model muse-spark [--stream=true]
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
- `GET /files/{id}`：管理会话或有效外部 key 认证；文件名强制附件下载并限制路径。
- `GET /v1/models`、`POST /v1/chat/completions`：Bearer API key；支持 `muse-spark`、`muse-image`、`muse-video`、`gpt-4o-mini`，流式返回 OpenAI SSE，非流式返回 `chat.completion`。
- `POST /api/chat`、`POST /api/chat/stream`：简单同步/SSE 别名；API session 按 key 隔离。

错误只返回稳定的 code/message，不转发上游 body、URL、cookie 或 token。API key 与管理 console 会话永不互相替代。

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
