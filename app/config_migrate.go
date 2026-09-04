package app

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"log/slog"

	"github.com/goccy/go-yaml"
)

// v1/v2 统一视图：json/js 可能是字符串，也可能是列表
type singBoxConfigUnified struct {
	Version string `yaml:"version"`
	JSON    any    `yaml:"json"`
	JS      any    `yaml:"js"`
}

// 仅解析迁移相关字段
type migrateConfigView struct {
	SingboxLatest singBoxConfigUnified `yaml:"singbox-latest"`
	SingboxOld    singBoxConfigUnified `yaml:"singbox-old"`

	SubProcess struct {
		ResolveDomain any `yaml:"resolve-domain"`
	} `yaml:"sub-process"`

	MihomoOverwriteURL string `yaml:"mihomo-overwrite-url"`
}

// singBoxConfigV1 仅用于重写 YAML 文本（json/js 统一为 []string）
type singBoxConfigV1 struct {
	Version string   `yaml:"version"`
	JSON    []string `yaml:"json"`
	JS      []string `yaml:"js"`
}

// migrateConfig 检测配置文件中的旧格式字段，若存在则原地升级并保存。
func (app *App) migrateConfig() error {
	data, err := os.ReadFile(app.configPath)
	if err != nil {
		return nil
	}

	content := string(data)
	needWrite := false

	// 记录迁移类别
	migrated := []string{}

	var view migrateConfigView
	_ = yaml.Unmarshal(data, &view)

	// 自动升级 singbox-latest 到 1.14（支持 v1/v2）
	if view.SingboxLatest.Version != "" {
		latestVer := strings.TrimSpace(view.SingboxLatest.Version)
		latestJSON := extractString(view.SingboxLatest.JSON)
		latestJS := extractString(view.SingboxLatest.JS)

		isOfficialJSON := strings.Contains(latestJSON, "sinspired/sub-store-template")
		isOfficialJS := strings.Contains(latestJS, "sinspired/sub-store-template")

		major, minor := parseVersion(latestVer)

		needUpgrade :=
			versionLess(latestVer, 1, 14) ||
				(isOfficialJSON && (major != 1 || minor != 14)) ||
				(isOfficialJS && (major != 1 || minor != 14))

		if needUpgrade {
			slog.Info("singbox-latest 配置迁移到 1.14.x")

			newCfg := singBoxConfigV1{
				Version: "1.14",
				JSON: []string{
					"https://raw.githubusercontent.com/sinspired/sub-store-template/main/1.14.x/sing-box.json",
				},
				JS: []string{
					"https://raw.githubusercontent.com/sinspired/sub-store-template/main/1.14.x/sing-box.js",
				},
			}

			content = rewriteSingboxBlock(content, "singbox-latest", newCfg)
			needWrite = true
			migrated = append(migrated, "singbox")
		}

	}

	// singbox-old：仅当版本 ≤ 1.11 才提示用户
	if view.SingboxOld.Version != "" {
		oldMajor, oldMinor := parseVersion(view.SingboxOld.Version)

		// 用户主动填写 >1.11 的版本（例如 1.12），说明有需求 → 不提示
		if oldMajor == 1 && oldMinor <= 11 {
			slog.Warn("singbox-old 版本 1.11 已从 App Store 下架")
			slog.Info("sing-box MT 版本 1.14 已于 2026-8-31 上架 App Store")
			slog.Info("建议尽快移除 sing-box 1.11 配置，使用新版本")
		}
	}

	// resolve-domain 布尔 → 新对象格式迁移（含注释）
	// 这里用统一视图判断类型：bool = 旧格式；map = 新格式
	switch v := view.SubProcess.ResolveDomain.(type) {
	case bool:
		content = rewriteResolveDomain(content, v)
		needWrite = true
		migrated = append(migrated, "resolve-domain")
	case map[string]any, map[any]any:
		// 已是新对象格式，跳过
	default:
		// 不存在或其他类型，跳过
	}

	// 迁移 mihomo-overwrite-url 文件名
	if view.MihomoOverwriteURL != "" {
		old := view.MihomoOverwriteURL

		// 只替换文件名，不动前缀
		new := old

		if before, ok := strings.CutSuffix(old, "Sinspired_Rules_CDN.yaml"); ok {
			new = before + "Mihomo-Rules-CDN.yaml"
		} else if strings.HasSuffix(old, "Sinspired_Rules_Lite_CDN.yaml") {
			new = strings.TrimSuffix(old, "Sinspired_Rules_Lite_CDN.yaml") + "Mihomo-Rules-Lite-CDN.yaml"
		}

		if new != old {
			content = rewriteMihomoOverwriteURL(content, new)
			needWrite = true
			migrated = append(migrated, "mihomo-overwrite-url")
			slog.Debug("mihomo-overwrite-url 已迁移为新规则文件名")
		}
	}

	// 写回文件
	if needWrite {
		if err := os.WriteFile(app.configPath, []byte(content), 0o644); err != nil {
			slog.Error("配置文件迁移失败", "error", err)
			return err
		}
		joined := ""
		if len(migrated) == 1 {
			joined = migrated[0]
		} else if len(migrated) > 1 {
			joined = strings.Join(migrated, ",")
		}

		slog.Info("配置文件迁移完成", "config", joined)
	}

	return nil
}

// parseVersion 提取主版本号（major.minor）
// 支持：1.14 / 1.14.x / 1.14.33 / v1.14 / v1.14.12 / v1.14
func parseVersion(raw string) (major int, minor int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0
	}

	// 提取数字版本：匹配 1.14 / v1.14 / 1.14.x / 1.14.33
	re := regexp.MustCompile(`(\d+)\.(\d+)`)
	m := re.FindStringSubmatch(raw)
	if len(m) < 3 {
		return 0, 0
	}

	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return
}

// versionLess 判断是否低于目标版本（major.minor）
func versionLess(raw string, targetMajor, targetMinor int) bool {
	major, minor := parseVersion(raw)
	if major == 0 && minor == 0 {
		return true // 无效版本 → 视为过旧
	}
	if major < targetMajor {
		return true
	}
	if major == targetMajor && minor < targetMinor {
		return true
	}
	return false
}

// 提取字符串或列表的第一个元素
func extractString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []any:
		if len(val) > 0 {
			if s, ok := val[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

// rewriteSingboxBlock 在原始 yaml 文本中找到指定 singbox 块，
// 将其 json/js 写法替换为字符串写法，其余内容保持不变。
func rewriteSingboxBlock(content, blockKey string, v1 singBoxConfigV1) string {
	lines := strings.Split(content, "\n")

	// 找到块的起始行
	blockStart := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == blockKey+":" {
			blockStart = i
			break
		}
	}
	if blockStart < 0 {
		return content
	}

	blockIndent := len(lines[blockStart]) - len(strings.TrimLeft(lines[blockStart], " \t"))

	for i := blockStart + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent <= blockIndent {
			break
		}

		trimmed := strings.TrimSpace(line)
		keyIndent := strings.Repeat(" ", indent)

		switch {
		case strings.HasPrefix(trimmed, "version:"):
			lines[i] = keyIndent + "version: \"" + v1.Version + "\""

		case strings.HasPrefix(trimmed, "json:"):
			lines[i] = keyIndent + "json: " + v1.JSON[0]

		case strings.HasPrefix(trimmed, "js:"):
			lines[i] = keyIndent + "js: " + v1.JS[0]

		default:
			continue
		}
	}

	return strings.Join(lines, "\n")
}

// rewriteResolveDomain resolve-domain: bool → 新对象格式迁移（标准 YAML 注释风格）
func rewriteResolveDomain(content string, oldValue bool) string {
	lines := strings.Split(content, "\n")

	// 找到 sub-process 块
	blockStart := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "sub-process:" {
			blockStart = i
			break
		}
	}
	if blockStart < 0 {
		return content
	}

	blockIndent := len(lines[blockStart]) - len(strings.TrimLeft(lines[blockStart], " \t"))

	for i := blockStart + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent <= blockIndent {
			break
		}

		if strings.HasPrefix(strings.TrimSpace(line), "resolve-domain:") {

			keyIndent := strings.Repeat(" ", indent)

			newBlock := []string{
				keyIndent + "  # DNS 解析操作",
				keyIndent + "resolve-domain:",
				keyIndent + "  # 是否开启 DNS 解析",
				keyIndent + "  enable: " + strings.ToLower(strconv.FormatBool(oldValue)),
				keyIndent + "  # DNS 服务商（ali / cloudflare / google）",
				keyIndent + "  provider: ali",
				keyIndent + "  # 并发数，默认10，在代理软件中不要超过20",
				keyIndent + "  concurrency: 10",
				keyIndent + "  # 超时（毫秒），默认8000",
				keyIndent + "  timeout: 8000",
				keyIndent + "  # EDNS 设置",
				keyIndent + "  edns: \"\"",
				keyIndent + "  # 解析类型 ipv4 / ipv6",
				keyIndent + "  type: ipv4",
				keyIndent + "  # 缓存策略",
				keyIndent + "  cache: enable",
				keyIndent + "  # 缓存时长(秒)",
				keyIndent + "  cache-ttl: 3600",
			}

			lines[i] = newBlock[0]

			newLines := append([]string{}, lines[:i+1]...)
			newLines = append(newLines, newBlock[1:]...)
			newLines = append(newLines, lines[i+1:]...)
			return strings.Join(newLines, "\n")
		}
	}

	return content
}

// rewriteMihomoOverwriteURL 在原始 YAML 文本中替换 mihomo-overwrite-url 的值
func rewriteMihomoOverwriteURL(content, newURL string) string {
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "mihomo-overwrite-url:") {

			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			keyIndent := strings.Repeat(" ", indent)

			lines[i] = keyIndent + "mihomo-overwrite-url: " + newURL
			return strings.Join(lines, "\n")
		}
	}

	return content
}
