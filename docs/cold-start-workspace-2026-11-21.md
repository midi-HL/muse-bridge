# UNKNOWN 工作区的冷启动修复（2026-11-21）

在 ARM64 Debian / Android 容器部署时，真实新连接复现了 v0.1.38 的一个缺口：session 返回 assigned / UNKNOWN，token 为 200，但 Noise 网关升级返回 502。直连与既有代理对照一致；不是 ARM 加密失败，失败发生在 Noise 加密开始前。

一次正常的 /api/hatch/vm/wake 请求返回 200 / wake_requested 后，同一凭据的 token 保持 200、首次 WebSocket 升级立即恢复 101，正式程序的加密 ping 与真实聊天随后成功。未租用或替换工作区。

v0.1.38 正确放开了 UNKNOWN 的连接尝试，但同时跳过了正常 wake。此前测试在已被唤醒的工作区上通过，未覆盖隔夜休眠后的新连接。现在区分两个判断：UNKNOWN 仍可尝试真实传输，同时先请求一次正常唤醒；不要求控制面必须变成 RUNNING。其他未就绪状态继续沿用有界状态轮询。

唤醒确认也可能早于网关实际可达。对 UNKNOWN 工作区的 WebSocket 升级 502/503 增加 25 秒退避窗口，每次重新获取连接 ticket；全部 I/O 仍受 Connect 总计 45 秒及调用方更短 context 的限制。25 秒是退避窗口而非每次网络请求的独立硬超时。不重放 Noise 或生成消息，认证、限流及其他升级错误立即返回。

回归先在旧版本失败，再验证修复：冷网关在 wake 前返回 502、wake 后使用现有真实 Noise mock 完成 Probe 和 Generate。正常路径断言一次 session、一次 wake、一次 token 和一次 WS，Probe 保留原始 UNKNOWN。另覆盖 wake 401/429 立即停止、wake 503 但已运行网关仍可连接，以及网关连续 502/503 后恢复、不可重试的升级状态、永久未就绪被短 context 截止。

此次未增加未经证实的 auth/check 或 Bearer 刷新行为。调查中 curl 缺少 Sec-Fetch 请求头会单独引起 token 403；补齐现有 Go 客户端已有请求头即可获得 token，故未将该诊断差异混入生产修复。
