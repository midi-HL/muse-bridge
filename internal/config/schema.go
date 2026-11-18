// Package config defines the shared WebUI, CLI and agent settings contract.
package config

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/url"
	"os"
	"strings"
	"time"
)

type Field struct {
	Key           string   `json:"key"`
	Type          string   `json:"type"`
	Label         string   `json:"label"`
	LabelEN       string   `json:"label_en"`
	Description   string   `json:"description,omitempty"`
	DescriptionEN string   `json:"description_en,omitempty"`
	Group         string   `json:"group"`
	Secret        bool     `json:"secret,omitempty"`
	Default       any      `json:"default"`
	Options       []string `json:"options,omitempty"`
	Min           *float64 `json:"min,omitempty"`
	Max           *float64 `json:"max,omitempty"`
}

func text(k, l, en, g, d string) Field {
	return Field{Key: k, Type: "string", Label: l, LabelEN: en, Group: g, Default: d}
}
func secret(k, l, en, g string) Field { f := text(k, l, en, g, ""); f.Secret = true; return f }
func choice(k, l, en, g, d string, opts ...string) Field {
	f := text(k, l, en, g, d)
	f.Type = "select"
	f.Options = opts
	return f
}
func flag(k, l, en, g string, d bool) Field {
	return Field{Key: k, Type: "boolean", Label: l, LabelEN: en, Group: g, Default: d}
}
func number(k, l, en, g string, d, lo, hi float64) Field {
	return Field{Key: k, Type: "number", Label: l, LabelEN: en, Group: g, Default: d, Min: &lo, Max: &hi}
}

var Fields = []Field{
	text("general.timezone", "时区", "Time zone", "general", "Asia/Kuala_Lumpur"),
	choice("general.language", "界面语言", "Language", "general", "zh", "zh", "en"),
	choice("general.theme", "界面主题", "Theme", "general", "system", "system", "light", "dark"),
	flag("web.enabled", "启用网页聊天", "Enable web chat", "platforms", true),
	flag("telegram.enabled", "启用 Telegram", "Enable Telegram", "platforms", false),
	secret("telegram.token", "Bot Token", "Bot token", "platforms"),
	text("telegram.bot_username", "Bot 用户名（群命令目标验证）", "Bot username (group command target validation)", "platforms", ""),
	choice("telegram.mode", "接收模式", "Update mode", "platforms", "polling", "polling", "webhook"),
	text("telegram.webhook_url", "Webhook 地址", "Webhook URL", "platforms", ""),
	secret("telegram.webhook_secret", "Webhook 验证密钥", "Webhook secret", "platforms"),
	flag("telegram.whitelist_enabled", "启用白名单", "Require allowlist", "platforms", true),
	text("telegram.whitelist", "允许的用户 ID（逗号分隔）", "Allowed user IDs (comma separated)", "platforms", ""),
	number("telegram.edit_interval_ms", "流式编辑间隔（毫秒）", "Stream edit interval (ms)", "platforms", 1200, 1000, 10000),
	number("telegram.document_threshold", "长文转文件阈值（字符）", "Long response file threshold", "platforms", 16000, 4096, 200000),
	number("limits.concurrency", "全局并发数", "Global concurrency", "limits", 2, 1, 32),
	number("limits.queue_size", "最大排队数", "Maximum queue length", "limits", 20, 0, 200),
	number("limits.daily_per_user", "每用户每日请求上限（0 不限）", "Daily requests per user (0 unlimited)", "limits", 100, 0, 100000),
	number("limits.request_timeout_seconds", "生成超时（秒）", "Generation timeout (seconds)", "limits", 300, 30, 1800),
	flag("pool.split", "聊天与 API 分池", "Separate chat and API pools", "accounts", false),
	number("pool.probe_interval_seconds", "周期探活间隔（秒）", "Health check interval (seconds)", "accounts", 600, 60, 86400),
	number("pool.cooldown_seconds", "失败冷却（秒）", "Failure cooldown (seconds)", "accounts", 90, 10, 3600),
	flag("nodes.auto_select", "自动选择健康节点", "Automatically select healthy node", "nodes", false),
	choice("asr.provider", "语音识别来源", "Speech recognition provider", "asr", "local", "local", "cloud", "disabled"),
	text("asr.executable", "sherpa-onnx 可执行文件路径", "sherpa-onnx executable path", "asr", ""),
	text("asr.model_dir", "本地模型目录", "Local model directory", "asr", ""),
	text("asr.arguments", "参数 JSON 数组（{audio} 与 {model_dir} 占位）", "Arguments JSON array ({audio}, {model_dir})", "asr", "[\"--tokens={model_dir}/tokens.txt\",\"--sense-voice-model={model_dir}/model.int8.onnx\",\"{audio}\"]"),
	text("asr.cloud_url", "云端转写接口", "Cloud transcription endpoint", "asr", "https://api.openai.com/v1/audio/transcriptions"),
	secret("asr.cloud_key", "云端 API Key", "Cloud API key", "asr"),
	text("asr.cloud_model", "云端模型", "Cloud model", "asr", "whisper-1"),
	text("asr.language", "识别语言（auto 自动）", "Speech language (auto)", "asr", "auto"),
	number("asr.timeout_seconds", "转写超时（秒）", "Transcription timeout (seconds)", "asr", 120, 10, 600),
	text("media.ffmpeg", "ffmpeg 路径", "ffmpeg path", "media", "ffmpeg"),
	choice("media.video_mode", "视频处理方式", "Video processing", "media", "frames", "frames", "vision", "disabled"),
	number("media.video_frames", "视频抽帧数", "Video frame count", "media", 4, 1, 10),
	number("media.max_mb", "单附件上限（MiB）", "Attachment limit (MiB)", "media", 40, 1, 40),
	number("media.retention_hours", "生成物保留（小时）", "Generated file retention (hours)", "media", 72, 1, 720),
	text("vision.url", "视觉模型 Chat Completions 地址", "Vision chat completions endpoint", "media", "https://api.openai.com/v1/chat/completions"),
	secret("vision.key", "视觉模型 API Key", "Vision API key", "media"),
	text("vision.model", "视觉模型", "Vision model", "media", "gpt-4o-mini"),
	flag("stickers.enabled", "启用贴纸出站", "Enable outbound stickers", "stickers", true),
	text("persona.default_id", "默认人设 ID", "Default persona ID", "personas", ""),
	flag("alerts.enabled", "启用告警", "Enable alerts", "alerts", false),
	text("alerts.telegram_chat_id", "管理员聊天 ID", "Administrator chat ID", "alerts", ""),
	secret("alerts.webhook_url", "告警 Webhook URL", "Alert webhook URL", "alerts"),
	number("alerts.cooldown_seconds", "重复告警间隔（秒）", "Repeated alert interval (seconds)", "alerts", 300, 30, 86400),
	number("logs.retention_days", "日志保留（天）", "Log retention (days)", "general", 7, 1, 90),
}

func Defaults() map[string]any {
	v := map[string]any{}
	for _, f := range Fields {
		v[f.Key] = f.Default
	}
	return v
}
func Find(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}
func Validate(values map[string]any) error {
	for k, v := range values {
		f, ok := Find(k)
		if !ok {
			return fmt.Errorf("unknown setting: %s", k)
		}
		switch f.Type {
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s: boolean required", k)
			}
		case "number":
			n, ok := v.(float64)
			if !ok {
				return fmt.Errorf("%s: number required", k)
			}
			if n != float64(int64(n)) || n < *f.Min || n > *f.Max {
				return fmt.Errorf("%s: integer must be between %.0f and %.0f", k, *f.Min, *f.Max)
			}
		default:
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s: text required", k)
			}
			if len(s) > 32768 {
				return fmt.Errorf("%s: value too long", k)
			}
			if strings.ContainsAny(s, "\x00\r") {
				return fmt.Errorf("%s: invalid control characters", k)
			}
			if len(f.Options) > 0 {
				valid := false
				for _, o := range f.Options {
					valid = valid || s == o
				}
				if !valid {
					return fmt.Errorf("%s: invalid option", k)
				}
			}
			if k == "general.timezone" {
				if _, err := time.LoadLocation(s); err != nil {
					return fmt.Errorf("%s: unknown time zone", k)
				}
			}
			if strings.HasSuffix(k, "url") && s != "" {
				u, e := url.Parse(s)
				if e != nil || u.Host == "" || u.Scheme != "https" || u.User != nil {
					return fmt.Errorf("%s: HTTPS URL without user info required", k)
				}
			}
			if k == "asr.arguments" {
				var a []string
				if json.Unmarshal([]byte(s), &a) != nil || len(a) > 32 {
					return fmt.Errorf("%s: JSON array of at most 32 arguments required", k)
				}
			}
		}
	}
	return nil
}

// Load combines YAML with environment variables. Secret settings must use env.
func Load(path string) (map[string]any, []string, error) {
	values := map[string]any{}
	warnings := []string{}
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, nil, e
		}
		var raw map[string]any
		if e = yaml.Unmarshal(b, &raw); e != nil {
			return nil, nil, fmt.Errorf("invalid YAML configuration")
		}
		flatten("", raw, values)
	}
	for k, v := range values {
		f, ok := Find(k)
		if !ok {
			warnings = append(warnings, "unknown configuration field: "+k)
			delete(values, k)
			continue
		}
		if f.Secret {
			return nil, nil, fmt.Errorf("%s: secrets must use environment variables or encrypted settings", k)
		}
		if n, ok := v.(int); ok {
			values[k] = float64(n)
		}
	}
	for _, f := range Fields {
		name := "MUSE_" + strings.ToUpper(strings.ReplaceAll(f.Key, ".", "_"))
		if s, ok := os.LookupEnv(name); ok {
			var v any = s
			if f.Type == "boolean" || f.Type == "number" {
				if json.Unmarshal([]byte(s), &v) != nil {
					return nil, nil, fmt.Errorf("invalid environment value: %s", name)
				}
			}
			values[f.Key] = v
		}
	}
	return values, warnings, Validate(values)
}
func flatten(prefix string, raw map[string]any, out map[string]any) {
	for k, v := range raw {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if m, ok := v.(map[string]any); ok {
			flatten(key, m, out)
		} else {
			out[key] = v
		}
	}
}
