# 部署与测试报告

## 版本与环境

- 仓库：`midi-HL/muse-bridge`
- 提交：`b33fb06`（将 Go 标准库锁定到 `go 1.27.4`）
- Release：`v0.1.1` 已由 GitHub Actions 成功生成，七个矩阵目标均通过并发布 SHA-256 校验文件。此前 `v0.1.0` Release 也已成功生成。
- 本机：Windows amd64，Go 1.27.4；CGO-free 发布构建。
- 数据目录：本地冒烟使用隔离临时目录，测试后停止服务；未写入仓库。

## 本地自动化结果

| 检查 | 结果 | 备注 |
|---|---|---|
| `gofmt -w cmd internal web` | 通过 | 无格式残留 |
| `go vet ./...` | 通过 | Telegram JSON tag 重复问题已修复 |
| `go test -count=1 ./...` | 通过 | 协议、core、HTTP、CLI、媒体、Telegram 均编译测试 |
| `CGO_ENABLED=0 go build -trimpath ./cmd/muse-bridge` | 通过 | 发布约束保持纯 Go |
| `go test -race ./...` | 本机未执行 | Windows 环境无 gcc；CI 设为 `CGO_ENABLED=1` |
| `govulncheck ./...` | 首轮失败后修复 | Go 1.27.0 标准库漏洞报告促成升级到 1.27.4；v0.1.1 CI 重新验证 |

协议包的离线测试覆盖：Noise XX 双端握手、加密帧、乱序分片、重复分片拒绝、订阅事件、取消、上传大小校验、下载 EOF/长度校验。

## 本地 HTTP 冒烟

1. 全新数据目录启动：通过，监听 `127.0.0.1:18787`。
2. `GET /admin/api/auth/status`：返回 `setup_required=true`，未设置 Cookie。
3. `POST /admin/api/auth/setup`：通过，创建管理员会话和 CSRF token。
4. `GET /admin/api/schema`：通过。
5. `GET /admin/api/state`：通过；secret 字段只返回 `configured/masked`。
6. `POST /admin/api/action` 的 `keys.create`：通过，完整 API key 只在创建响应出现一次。
7. 不同来源、无 CSRF 和未认证请求：由 HTTP 安全测试覆盖并拒绝。

## 真实账号与外部依赖

- 真实 Cookie 未写入源码、日志、报告或 CI。
- 对提供的 Cookie 做过一次隔离 HTTP 导入尝试，HTTP 层返回泛化 400，未把它误判为 `invalid`，也未输出 Cookie。随后用同一文件内容做临时 core 导入诊断，核心解析和加密入库通过；因此问题尚未闭合到可宣称的真实协议探活。
- 未提供 Telegram Bot token、ASR 模型/云端密钥，Telegram、真实 Muse 回复、媒体生成、ASR、节点可用性和 ASUS 笔记本正式产物部署不能在本环境宣称通过。

## 18 项验收状态

| # | 用例 | 状态 |
|---:|---|---|
| 1 | 全新部署与首次设密 | 本地通过 |
| 2 | 真实 Cookie 探活与额度 | 未闭合；不宣称通过 |
| 3 | Web 聊天真实回复 | 未闭合；需要真实上游账号 |
| 4 | Telegram token + 白名单 | 未执行；缺 token |
| 5 | Telegram 图片 | 未执行；缺 token/真实上游 |
| 6 | Telegram 文件 | 未执行；缺 token/真实上游 |
| 7 | Telegram 贴纸 | 未执行；缺 token |
| 8 | 图片生成 | 协议模拟通过，真实外部未执行 |
| 9 | `/new` `/stop` `/retry` `/status` `/help` | Web/core stop 和 Telegram基础命令已实现；外部未执行 |
| 10 | `/tasks` 等协议能力 | 明确 unsupported/unknown，无参数证据 |
| 11 | 群聊发言人归属 | 适配层字段与前缀边界已实现，外部未执行 |
| 12 | OpenAI models/chat completions | HTTP 合同和安全测试通过，真实上游未执行 |
| 13 | 失效 Cookie 状态与告警 | 状态映射已实现，真实失效账号未执行 |
| 14 | 重启持久化 | SQLite/settings/session 单元测试通过 |
| 15 | 非白名单不响应 | Telegram 白名单逻辑已实现，外部未执行 |
| 16 | 长回复拆分/文件 | UI/API边界和 Telegram UTF-16 rune 拆分已实现，外部未执行 |
| 17 | ASR、视频、贴纸出站、节点、定时消息 | 组件/操作注册表已建立；部分动作仍 unsupported，外部未执行 |
| 18 | 管理台逐页与移动端视觉走查 | 代码和响应式 CSS 已完成；当前工具环境未提供可用浏览器控制器，不能伪造视觉通过 |

结论：自动化和安全边界已通过；真实 Muse/Telegram/ASR/目标笔记本部署必须由部署者在具备对应凭证和网络环境的机器上完成，不能用 CI 模拟结果替代。
