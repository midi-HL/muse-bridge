# Muse 客户端能力对照表

| 能力 | 协议层证据 | Web / 命令入口 | 结论 |
|---|---|---|---|
| 文本生成 | `POST /chat/subscribe`、`POST /chat/stream`，Noise XX + protobuf 帧 | `/admin/api/chat`、`/v1/chat/completions`、`muse-bridge chat` | 已实现；2026-11-20 原导入 Cookie 经修复后的协议客户端真实回复通过，正式部署入口验收见真实聊天报告 |
| 流式更新 | `delta.text_append`、`delta.message_done` 事件 | 管理台 SSE、Telegram适配层、OpenAI SSE | 已实现；Telegram/API平台限制可能不同 |
| 图片/视频生成 | 同一 `chat/stream` 的模型别名与 presentation 资源 | Web聊天/API模型选择 | 图片生成与文件取回于 2026-11-20 真实验证；视频仍需真实验收。媒体只接收可归属本轮任务的事件 |
| 文件上传 | `/fs/write`、`/fs/stat`、`/fs/rename`，分片写入并校验大小 | Web附件、core Chat | 已实现并有模拟测试 |
| 生成物下载 | `/fs/read`，分片读取与 EOF/长度校验 | `/files/{id}`、Web聊天附件 | 已实现 |
| 心跳 | `/api/ping` | 周期连接保活 | 已实现 |
| VM唤醒/令牌 | `/api/session?vm_state=live`、`/api/hatch/vm/wake`、`/api/hatch/token` | 账号探活/协议自检 | 已实现 |
| 重连/账号轮换 | 参考实现的轮换和指数重连；本项目pool每账号锁+轮换 | 账号池/API/聊天 | 已实现基础轮换与故障状态 |
| 本地会话列表、切换、历史和改名 | SQLite 中当前聊天的本地映射与历史 | Telegram `/sessions`、`/switch ID`、`/history`、`/rename 名称`，管理台 `sessions.*` | 本地会话管理已接通 Telegram；跨聊天拒绝，切换保存原创建者，生成中拒绝改名。Webhook、重开数据库和备份恢复测试通过 |
| 远端会话列表、历史 | 参考纯协议代码未提供可核验路由或参数 | 无可靠远端入口 | 能力未知；本地会话命令不等于读取官方客户端远端记录 |
| Agent待办/任务列表 | 文档提到 `chat.stream_events` 等候选方法，但无可靠参数及实现 | `/tasks` 未假装可用 | 上游协议证据不足，明确 unsupported |
| 群聊触发与发言人 | Telegram 提及实体、回复对象和 `getMe` 确定触发，发言人作为文本上下文传入 Muse | 群内 @ Bot、回复 Bot、`/retry` | 模拟轮询和 Webhook 通过，模型输入与成功历史保留发言人。真实群聊待验收 |
| 远端停止生成 | 文档有 `cancel` 候选名，无可核验路径/参数 | `/stop` / `chat.stop` | 仅取消本地等待和连接；不宣称远端计算已停止 |
| 本地每日聊天额度 | 本程序 Core SQLite 计数，按配置时区、Telegram 发送者/匿名 chat 归属 | Telegram `/usage` | 已实现只读查询已用/上限/剩余/重置时刻；不是 Muse 官方余额 |
| Muse 账号额度/用量 | 参考实现通过浏览器 Settings UI 读取，不属于纯协议路由 | 仪表盘额度字段保留未知 | 纯协议层不可验证，显示 unknown，不填假数据 |
| 人设 | 作为本地文本上下文注入；不能移除 Muse 系统提示词 | `persona.*`、Web聊天 | 尽力而为，已在UI说明 |
| 贴纸/ASR/视频处理 | 业务层能力，不是 Hatch 协议能力 | 独立 media/Telegram 包 | 已提供安全边界和可配置实现；真实外部服务需部署机验收 |

## 依据与限制

协议实现逐项对照 `lichao199208/muse2api/app/muse_engine/protocol_client.py`、`protocol_engine.py` 与 `docs/noise-protocol.md`。文档中列出的候选方法不会因为名字相似就被当成已支持接口。CI 只运行模拟上游，绝不使用真实 Cookie。
