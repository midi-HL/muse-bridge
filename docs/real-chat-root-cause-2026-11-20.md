# 真实聊天链路根因与修复（2026-11-20）

基线：v0.1.37。此次使用已登录官网、用户原先导入的 Cookie 和新导出的本机 Cookie 进行对照；所有凭据及原始网络材料仅留在被 Git 忽略的本机诊断目录，不进入代码、CI 或本文。

## 已复现的故障与根因

已安装程序的 `/admin/api/chat` 在约 29 秒后返回 SSE `error / pending_vm`，没有助手文本。HTTP 200 仅代表 SSE 建立，不能作为聊天成功证据。同一时间官网刷新后仍可真实回复 `MUSE_BROWSER_OK`。

同一可用工作区的 `/api/session` 与 `/api/session?vm_state=live` 均返回 `assigned / UNKNOWN`，VM 与 endpoint 字段完整。三 Cookie 和完整 Cookie 对照没有改变此结果。原客户端只接受空状态或 `RUNNING`，因此在获取 token、Noise 握手之前就轮询并退出。`UNKNOWN` 是控制面未观测到状态，不能等价为网关停机。

单变量诊断仅改变本地的该判断后，token 为 200、Noise 为 101、加密 ping 通过，真实生成回复 `BRIDGE_DIAG_OK`。生产修复只将完整已分配工作区的 `UNKNOWN` 纳入可尝试连接状态；不改写上游状态，`Probe.VMState` 仍保留 `UNKNOWN`。认证、实际握手、ping、confidential VM 检查和其他停机状态检查继续保留。

原先导入的 Cookie 也通过修复后的真实聊天，并非只有重新导出的 Cookie 可用。此次证据不支持把当前聊天故障归因于 Cookie 导出格式。

## 真实测试同时发现的媒体归属问题

原订阅从零序号开始，会回放工作区历史。四条不含 `device_id` 的历史 `delta.presentation` 被当成本轮附件，纯文字回复附带了四张旧图；媒体模式还可能在旧图片或旧视频上提前完成。

修复后，事件需满足当前请求的 device 归属，或其 `message_id` 已由当前 device 的事件确认。明确来自其他 device 的事件始终丢弃。先到的无归属 presentation 最多保留 128 个媒体候选，确认消息窗口最多 256 条；随后确认同一 message ID 才会释放，避免历史资源串入当前结果。

## 真实账号与工作区对照

| 输入记录 | 上游状态 | 真实链路结果 | 说明 |
|---|---|---|---|
| 当前导入记录 / 官网新导出 | assigned / UNKNOWN | token 200、Noise 101、ping 与文字生成成功 | 原导入凭据也成功；源码修复后无需诊断覆盖 |
| 旧记录 1 | assigned / UNKNOWN | token 200、Noise 101、ping 成功 | 与当前官网选中同一个 VM，不能按不同 Cookie 会话数当成独立账号数 |
| 旧记录 2、3、4 | assigned / DISABLED | wake 409；受限直连对照的 token 200、Noise 502 | 各自单一工作区；移除可选 VM Cookie 或只保留 session Cookie 后仍不可达 |

对 DISABLED 的直连对照仅用于诊断一次，未改变服务器状态、租用/替换 VM 或生成消息。证据表明三个工作区的网关后端当前不可达；没有据此推断账号封禁。两种新 Cookie 导出是同一账号；目前本机是五条记录，不是六个已核验的独立账号。

## 验证

- 回归先在旧逻辑失败，再在修复后通过：UNKNOWN 完整 bootstrap → 真实 Noise mock → 加密 ping → Generate。保留原始 UNKNOWN，覆盖缺失连接字段及停机状态负例。
- 媒体回归经过同一加密 mock：历史图片/视频不得输出或提前结束；本轮媒体、先到后确认、异设备、缺少归属、300 条历史的有界窗口与重复媒体去重。
- 本机真实测试：原导入 Cookie 获得 `BRIDGE_DIAG_OK`，附件为 0；新生成图片并下载成功，确认为 1600 × 1600 WEBP，27,596 字节。真实测试与离线回归分开记录。
- 本机全库 Go 测试、vet、21 项前端行为测试通过；独立代码审查通过。正式安装仍使用 GitHub Actions 七平台产物，部署后需再次验证管理台、连续对话及 API。

## 部署环境与独立限制

当前部署监听 `http://127.0.0.1:8787/admin/`；18787 没有监听实例。管理员密码、加密主密钥、现有账号和运行数据保持原样。

Telegram 配置中白名单已启用但人数为零，这是独立的接入限制。修复 Muse 链路不等于真实 Telegram 入站已经验证，也不应为测试直接扩大机器人访问范围。

## 实现对照

参考了上层续签代码和新的主源实现；没有把它们的全部差异一次性移植。当前真实账号无需额外 lease、auth/check 或独立 notary 请求即可成功，因此此次只修正经实测证实的原因。

- `nikships/muse-cli`，`b62adbabb77abf688d26a74f93d405b6667dbc9f`，`src/muse_cli/gateway.py`：不以 RUNNING 标签阻断已分配 VM。
- `manishkumar1601/muse-ai-api`，`7c0d9d443f98a2fd37207d6730b70701ea85060e`，`src/bootstrap/session.ts`。
- 本地参考 `muse2api/app/muse_engine/engine.py`：lease-vm 确实参与上层续签，但此次所有失败样本已 assigned，不能单凭缺少 lease 推定根因。
