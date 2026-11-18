package httpapi

import (
	"github.com/midi-HL/muse-bridge/internal/config"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

type Operation struct {
	Name          string           `json:"name"`
	Title         string           `json:"title"`
	TitleEN       string           `json:"title_en"`
	Group         string           `json:"group"`
	Description   string           `json:"description"`
	DescriptionEN string           `json:"description_en"`
	Dangerous     bool             `json:"dangerous,omitempty"`
	Fields        []map[string]any `json:"fields"`
}

func field(key, label, kind string) map[string]any {
	return map[string]any{"key": key, "label": label, "type": kind}
}
func Operations() []Operation {
	id := field("id", "ID", "string")
	name := field("name", "名称 / Name", "string")
	pass := field("passphrase", "备份口令 / Passphrase", "password")
	otp := field("otp", "动态验证码 / TOTP", "string")
	specs := []Operation{
		{Name: "settings.update", Title: "修改配置", TitleEN: "Update settings", Group: "settings", Fields: []map[string]any{field("values", "配置值 / Values", "object")}},
		{Name: "accounts.list", Title: "列出账号", TitleEN: "List accounts", Group: "accounts"},
		{Name: "accounts.import", Title: "导入账号", TitleEN: "Import accounts", Group: "accounts", Fields: []map[string]any{field("text", "Cookie 或账号池 JSON", "textarea")}},
		{Name: "accounts.update", Title: "修改账号", TitleEN: "Update account", Group: "accounts", Fields: []map[string]any{id, field("label", "备注 / Label", "string"), field("enabled", "启用 / Enabled", "boolean"), field("node_id", "节点 ID", "string"), field("pool", "shared / chat / api", "string")}},
		{Name: "accounts.delete", Title: "删除账号", TitleEN: "Delete account", Group: "accounts", Dangerous: true, Fields: []map[string]any{id}},
		{Name: "accounts.probe", Title: "账号探活", TitleEN: "Probe account", Group: "accounts", Fields: []map[string]any{id}},
		{Name: "nodes.save", Title: "保存节点", TitleEN: "Save proxy node", Group: "nodes", Fields: []map[string]any{id, name, field("url", "代理 URL", "password")}},
		{Name: "nodes.delete", Title: "删除节点", TitleEN: "Delete node", Group: "nodes", Dangerous: true, Fields: []map[string]any{id}},
		{Name: "nodes.probe", Title: "节点测速", TitleEN: "Probe node", Group: "nodes", Fields: []map[string]any{id}},
		{Name: "nodes.test", Title: "节点连通性测试", TitleEN: "Test node", Group: "nodes", Fields: []map[string]any{id}},
		{Name: "personas.save", Title: "保存人设", TitleEN: "Save persona", Group: "personas", Fields: []map[string]any{id, name, field("prompt", "提示词 / Prompt", "textarea")}},
		{Name: "personas.delete", Title: "删除人设", TitleEN: "Delete persona", Group: "personas", Dangerous: true, Fields: []map[string]any{id}},
		{Name: "sessions.history", Title: "查看历史", TitleEN: "Session history", Group: "sessions", Fields: []map[string]any{id}},
		{Name: "sessions.reset", Title: "重置会话", TitleEN: "Reset session", Group: "sessions", Dangerous: true, Fields: []map[string]any{id}},
		{Name: "sessions.export", Title: "导出会话", TitleEN: "Export session", Group: "sessions", Fields: []map[string]any{id}},
		{Name: "sessions.rename", Title: "重命名会话", TitleEN: "Rename session", Group: "sessions", Fields: []map[string]any{id, name}},
		{Name: "chat.stop", Title: "停止生成", TitleEN: "Stop generation", Group: "sessions", Fields: []map[string]any{field("session_id", "会话 ID", "string")}},
		{Name: "keys.list", Title: "列出密钥", TitleEN: "List API keys", Group: "keys"},
		{Name: "keys.create", Title: "创建 API 密钥", TitleEN: "Create API key", Group: "keys", Fields: []map[string]any{name, field("rpm", "每分钟上限", "number"), field("daily", "每日上限", "number")}},
		{Name: "keys.update", Title: "修改 API 限额", TitleEN: "Update API limits", Group: "keys", Fields: []map[string]any{id, field("rpm", "每分钟上限", "number"), field("daily", "每日上限", "number")}},
		{Name: "keys.revoke", Title: "吊销 API 密钥", TitleEN: "Revoke API key", Group: "keys", Dangerous: true, Fields: []map[string]any{id}},
		{Name: "backup.export", Title: "加密导出", TitleEN: "Export encrypted backup", Group: "backup", Fields: []map[string]any{pass}},
		{Name: "backup.import", Title: "恢复备份", TitleEN: "Import encrypted backup", Group: "backup", Dangerous: true, Fields: []map[string]any{pass, field("blob", "加密备份 / Backup", "textarea")}},
		{Name: "auth.totp.setup", Title: "准备两步验证", TitleEN: "Prepare TOTP", Group: "auth"},
		{Name: "auth.totp.enable", Title: "启用两步验证", TitleEN: "Enable TOTP", Group: "auth", Fields: []map[string]any{otp}},
		{Name: "auth.totp.disable", Title: "关闭两步验证", TitleEN: "Disable TOTP", Group: "auth", Dangerous: true, Fields: []map[string]any{field("password", "当前密码", "password"), otp}},
		{Name: "auth.password", Title: "修改密码", TitleEN: "Change password", Group: "auth", Dangerous: true, Fields: []map[string]any{field("current_password", "当前密码", "password"), field("password", "新密码", "password"), otp}},
		{Name: "protocol.probe", Title: "协议自检", TitleEN: "Probe protocol", Group: "protocol"},
		{Name: "protocol.tasks", Title: "上游任务", TitleEN: "Upstream tasks", Group: "protocol"},
		{Name: "protocol.threads", Title: "上游会话", TitleEN: "Upstream threads", Group: "protocol"},
		{Name: "schedules.save", Title: "保存定时任务", TitleEN: "Save schedule", Group: "schedules", Fields: []map[string]any{id, name, field("enabled", "启用", "boolean"), field("platform", "平台", "string"), field("target", "目标", "string"), field("prompt", "提示词", "textarea"), field("cron", "Cron", "string"), field("timezone", "时区", "string")}},
		{Name: "schedules.delete", Title: "删除定时任务", TitleEN: "Delete schedule", Group: "schedules", Dangerous: true, Fields: []map[string]any{id}},
		{Name: "stickers.import", Title: "导入贴纸包", TitleEN: "Import sticker pack", Group: "stickers", Fields: []map[string]any{field("url", "贴纸包链接", "url")}},
		{Name: "stickers.update", Title: "修改贴纸描述", TitleEN: "Update sticker description", Group: "stickers", Fields: []map[string]any{id, field("description", "描述", "textarea")}},
		{Name: "stickers.delete", Title: "删除贴纸", TitleEN: "Delete sticker", Group: "stickers", Dangerous: true, Fields: []map[string]any{id}},
		{Name: "asr.test", Title: "测试语音转写", TitleEN: "Test transcription", Group: "asr", Fields: []map[string]any{field("filename", "文件名", "string"), field("data_base64", "音频 base64", "textarea")}},
		{Name: "asr.download", Title: "下载语音模型", TitleEN: "Download ASR model", Group: "asr", Fields: []map[string]any{field("url", "HTTPS 模型地址", "url"), field("sha256", "SHA-256（可选 / optional）", "string"), field("filename", "文件名 / Filename", "string")}},
	}
	for i := range specs {
		if specs[i].Fields == nil {
			specs[i].Fields = []map[string]any{}
		}
		if specs[i].Description == "" {
			specs[i].Description = specs[i].Title
		}
		if specs[i].DescriptionEN == "" {
			specs[i].DescriptionEN = specs[i].TitleEN
		}
	}
	return specs
}
func knownAction(name string) bool {
	for _, o := range Operations() {
		if o.Name == name {
			return true
		}
	}
	// Core may expose read-only aliases that are intentionally not rendered as forms.
	return name == "state" || name == "schema" || name == "settings" || name == "accounts" || name == "log"
}
func maskSettings(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if f, ok := config.Find(k); ok && f.Secret {
			if _, already := val.(map[string]any); already {
				out[k] = val
			} else {
				text, _ := val.(string)
				out[k] = secure.Mask(text)
			}
		} else if k == "settings" {
			out[k] = maskSettings(val)
		} else {
			out[k] = val
		}
	}
	return out
}
