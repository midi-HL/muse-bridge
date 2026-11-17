# Muse 客户端能力对照表

| 能力 | 协议层证据 | Web / 命令入口 | 结论 |
|---|---|---|---|
| 文本生成 | `POST /chat/subscribe`、`POST /chat/stream`，Noise XX + protobuf 帧 | `/admin/api/chat`、`/v1/chat/completions`、`muse-bridge chat` | 已实现并有离线双端模拟测试 |
| 流式更新 | `delta.text_append`、`delta.message_done` 事件 | 管理台 SSE、Telegram适配层、OpenAI SSE | 已实现；Telegram/API平台限制可能不同 |
| 图片/视频生成 | 同一 `chat/stream` 的模型别名与 presentation 资源 | Web聊天/API模型选择 | 已实现协议与文件取回；需真实账号验证资源形态 |
| 文件上传 | `/fs/write`、`/fs/stat`、`/fs/rename`，分片写入并校验大小 | Web附件、core Chat | 已实现并有模拟测试 |
| 生成物下载 | `/fs/read`，分片读取与 EOF/长度校验 | `/files/{id}`、Web聊天附件 | 已实现 |
| 心跳 | `/api/ping` | 周期连接保活 | 已实现 |
| VM唤醒/令牌 | `/api/session?vm_state=live`、`/api/hatch/vm/wake`、`/api/hatch/token` | 账号探活/协议自检 | 已实现 |
| 重连/账号轮换 | 参考实现的轮换和指数重连；本项目pool每账号锁+轮换 | 账号池/API/聊天 | 已实现基础轮换与故障状态 |
| 会话列表、远端历史 | 参考纯协议代码未提供可核验路由或参数 | 本地 `/sessions.*` | 远端能力未知；本地映射、历史、重命名、导出已实现 |
| Agent待办/任务列表 | 文档提到 `chat.stream_events` 等候选方法，但无可靠参数及实现 | `/tasks` 未假装可用 | 上游协议证据不足，明确 unsupported |
| 远端停止生成 | 文档有 `cancel` 候选名，无可核验路径/参数 | `/stop` / `chat.stop` | 仅取消本地等待和连接；不宣称远端计算已停止 |
| 额度/用量 | 参考实现通过浏览器 Settings UI 读取，不属于纯协议路由 | 仪表盘额度字段保留未知 | 纯协议层不可验证，显示 unknown，不填假数据 |
| 人设 | 作为本地文本上下文注入；不能移除 Muse 系统提示词 | `persona.*`、Web聊天 | 尽力而为，已在UI说明 |
| 贴纸/ASR/视频处理 | 业务层能力，不是 Hatch 协议能力 | 独立 media/Telegram 包 | 已提供安全边界和可配置实现；真实外部服务需部署机验收 |

## 依据与限制

协议实现逐项对照 `lichao199208/muse2api/app/muse_engine/protocol_client.py`、`protocol_engine.py` 与 `docs/noise-protocol.md`。文档中列出的候选方法不会因为名字相似就被当成已支持接口。CI 只运行模拟上游，绝不使用真实 Cookie。
