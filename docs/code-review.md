# 代码审查报告

日期：2026-11-18

## 范围

审查了 `cmd/`、`internal/`、`web/`、GitHub Actions 和文档。重点覆盖凭证边界、Noise/protobuf分片、HTTP认证、会话隔离、并发取消、媒体下载、前端输入与跨平台构建。

## 已确认并修复

1. 管理会话使用 HttpOnly、SameSite=Strict Cookie，并要求同源 Origin 与 CSRF token；非回环监听首次设置必须提供 `MUSE_SETUP_TOKEN`。
2. 管理密码使用 Argon2id；随机 API key 使用 SHA-256 摘要并存入 AES-GCM 加密字段，只在创建响应展示一次；外部 API key 与管理 Cookie 不互相替代。
3. Cookie、代理密码、API key 和 Bearer 内容通过 `secure.Redactor` 和固定错误映射脱敏；前端 secret 字段只展示 configured/masked。
4. 账号 secret 在 SQLite `secret` 列使用 AES-GCM，公开记录不含明文；配置持久化拆分 public/secret。
5. Noise 传输限制握手、protobuf字段、分片组数、分片总大小、重复分片、响应缓冲与逻辑 stream 数；连接失败释放等待者和连接。
6. 文件安全补丁将 HTTP 下载切换为 `os.Root` 打开的文件句柄：校验相对/旧绝对路径、拒绝链接和非普通文件，不在检查后按原路径重新打开；新生成物使用随机内部文件名，持久化失败清理孤儿文件。外部 API key 必须匹配生成物所属 key；旧无归属文件仅管理员可读。
7. API 会话 ID 按 key 摘要隔离；core 会验证本地 session 的 platform/user_id，防止跨用户读取历史。
8. Stop 只取消本地 context；代码和能力报告均不声称上游计算已停止。
9. 媒体公共下载要求 HTTPS、公网 DNS、禁止重定向、限制大小；ASR 子进程使用 `CommandContext` 且参数数组不经过 shell。
10. UI 使用 DOM textContent、表单校验、危险操作确认、响应式布局、深浅主题和中英文资源；密钥/凭证不会进入浏览器持久存储。

## 验证命令

| 命令 | 结果 |
|---|---|
| `gofmt -w cmd internal web` | 通过 |
| `go vet ./...` | 通过（修正 Telegram JSON tag 后复跑） |
| `go test -count=1 ./...` | 通过 |
| `go test ./internal/hatch` | 通过，含模拟 Noise 握手、分片、生成、上传、下载、取消 |
| `go test ./internal/core ./internal/pool ./internal/httpapi` | 通过，含 secret 掩码/持久化和 session 隔离 |
| `CGO_ENABLED=0 go build ./cmd/muse-bridge` | 通过 |
| `go test -race ./...` | 本机 Windows Go race 需要 CGO；CI 以 `CGO_ENABLED=1` 执行，发布仍 `CGO_ENABLED=0` |
| `govulncheck ./...` | 本轮源码提交 d6cd64e 的 CI 44242271552 已通过 |
| `go test -race -count=1 ./...` | 同一提交的 Ubuntu CI 已通过；本机没有 gcc |

## 文件与媒体安全补丁的失败证明与复验

- 修改前复现：文件目录中的符号链接可指向目录外；简单禁止绝对路径又使自导出的合法备份失败；`../outside.txt` 文件记录可通过备份校验并改写此前记录。修复后同组测试全部通过。
- HTTP 集成测试使用真实 core 后端与两个合成 API key，验证 GET/HEAD：所属 key 与管理员成功，其他 key 和目录外记录为 404，响应不含外部文件或宿主机路径。
- 新的模型下载 SHA-256 校验在写文件前执行；校验失败无落盘，成功只返回相对模型路径。
- 下载测试先复现未知/虚报 Content-Length 超限仍返回成功、流读取失败返回部分数据、错误携带 URL、DNS 不受总期限约束、继承环境代理等问题。修复后实际字节限制、取消、地址固定、重定向拒绝及资源关闭用例通过。
- 备份中合法旧绝对路径与新相对路径均覆盖测试；备份不含媒体字节，缺少字节的记录保持不可下载。独立审查指出跨数据目录旧备份迁移被拒，新增失败用例后修复为仅按受限旧文件布局取 basename 重定位到当前根目录，不访问旧路径；复验通过。此处未声称备份所有业务类型都具备完整事务隔离。

## v0.1.28 Telegram 会话、重试和轮询修复

- `/new` 指针由数据库作为唯一来源并按 chat 串行写入；旧 `telegram_<chatID>` 群聊首次访问时迁移历史 owner 到 chat ID，同时保留原创建者用于人设授权。
- 普通 Telegram 请求在 core 会话 lease 后记录 sender-specific attempt ID；`/retry` 必须携带仍然有效的 attempt snapshot。重试总是追加独立 turn，不按 prompt 文本裁剪历史，因此相同文本不会覆盖其他成功回答；reset 清除 retry 和命令结果。
- `/stop` 通过 core 扫描该 chat 的实际 lease，`/new` 后仍能停止旧生成。长轮询改为有界异步任务分派，控制命令可在生成中到达；取消会清理空 placeholder。`/command@other_bot` 在配置 bot username 时被忽略，匹配目标才执行。
- Webhook command 以 `(chat_id,message_id)` 持久化结果；重复投递复用结果，reset 与完成写回共享互斥，避免旧结果覆盖 invalidated tombstone。
- Telegram session/retry/command records 纳入备份 allowlist；新增 backup round-trip、migration、stale retry、same-text multi-sender、polling stop、target filtering 和 placeholder cleanup tests。

## v0.1.27 账号路由与人设修复

- 账号的 `node_id/pool/note` 原先只在界面提交，调度端没有对应字段；现已持久化并实际参与代理选择和分池。代理测试使用本地 HTTP 代理观察 bootstrap 请求，覆盖重启与节点 URL 更新；没有使用真实 Cookie 或外部节点。
- 开启分池时，API 仅使用 api/shared，Web/Telegram 仅使用 chat/shared。无合适账号立即失败；绑定节点无效不会绕过节点直连。账号更新先持久化再发布内存状态。
- 原先人设 ID 直接作为提示词，默认人设未生效。现将已启用预设正文传给生成接口，默认选择按会话保存，支持 `_none`；非法预设及生成失败不提交会话消息。相同会话的并发生成/切换不再覆盖历史。
- WebUI 实际组件测试覆盖人设选择、失败重试、定时刷新、异步切会话和生成期间禁用；CLI、OpenAI、简单 API、Web 与 Telegram 命令入口有行为测试。没有据此宣称浏览器视觉或真实 Muse 服从预设通过。
- 独立审查发现重复导入同 ID 会清空绑定/分池/备注并启用停用账号。新增失败测试后修为原子 Cookie 刷新，只有显式字段才改动；HTTP 与 core 测试均通过。
- 第二轮审查发现只改备注也会关闭正在使用的 client。真实延迟代理请求先复现 note/node/cookie 三种更新导致取消，再修为连接重建延后到当前请求完成；旧请求不能覆盖新凭证。两项 P2 经独立只读复审均关闭（verdict: pass）。
- 本轮本地 `go vet ./...`、完整 Go 测试、前端 12 项行为测试、纯 Go 构建与 diff 检查通过。独立最终 reviewer 会话多次异常终止，故不记录为独立最终 review pass；此前各 finding 按实际复现测试修复并在指定范围复验。
- v0.1.27 本地 `go vet ./...`、完整 Go 测试、前端 12 项行为测试、纯 Go 构建与 HTTP 启动冒烟已通过；CI [44265763306](https://github.com/midi-HL/muse-bridge/actions/runs/44265763306)（同源码提交）中 format/web/vet/unit/race/govulncheck/build 全过，Release [44283876645](https://github.com/midi-HL/muse-bridge/actions/runs/44283876645) 七平台全过。

## 残余风险和诚实限制

- 没有在 CI 或仓库使用真实 Muse Cookie，因此真实 Muse、Telegram Bot、ASR 云端和节点环境的 E2E 未被伪造为通过。
- 纯协议源码没有可核验的远端任务/待办、额度读取、线程列表和取消参数；这些功能报告为 unknown/unsupported。
- 媒体和 Telegram 适配包完成了安全边界和 Bot API 基础实现；主程序默认不启动未配置 token 的 Telegram 轮询，填写 token 后需在部署机执行外部验收。
- GitHub Actions 负责七平台编译；本地二进制只用于冒烟测试，不作为 Release 交付物。
