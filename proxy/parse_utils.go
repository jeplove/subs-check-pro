package proxies

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/samber/lo"
	"github.com/sinspired/subs-check-pro/utils"
)

var (
	v2rayRegexOnce         sync.Once
	v2rayLinkRegexCompiled *regexp.Regexp
)

// 节点去重
func deduplicateNodes(nodes []ProxyNode) []ProxyNode {
	if len(nodes) == 0 {
		return nodes
	}
	seen := make(map[string]struct{}, len(nodes))
	out := nodes[:0] // 原地复用，减少分配
	for _, n := range nodes {
		k := GenerateProxyKey(n)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, n)
	}
	return out
}

// ExtractV2RayLinks 正则提取逻辑
func ExtractV2RayLinks(data []byte) []string {
	var links []string
	v2rayRegexOnce.Do(func() {
		// 动态构建正则，匹配所有已知协议头
		schemes := make([]string, 0, len(protocolSchemes))
		seen := make(map[string]bool)
		for _, p := range protocolSchemes {
			s := strings.TrimSuffix(strings.ToLower(p), "://")
			if !seen[s] && s != "" {
				schemes = append(schemes, regexp.QuoteMeta(s))
				seen[s] = true
			}
		}
		// 模式: 单词边界 + 协议 + :// + 非空白/引号/括号字符
		pattern := `(?i)\b(` + strings.Join(schemes, `|`) + `)://[^\s"'<>\)\]]+`
		v2rayLinkRegexCompiled = regexp.MustCompile(pattern)
	})

	links = v2rayLinkRegexCompiled.FindAllString(string(data), -1)

	if len(links) == 0 {
		return links
	}

	// 简单清洗结果
	out := make([]string, 0, len(links))
	for _, s := range links {
		t := strings.Trim(s, "\"'`,;：")
		if t != "" {
			slog.Debug("正则捕获", "raw", s, "cleaned", t)
			out = append(out, t)
		}
	}
	return lo.Uniq(out)
}

// extractShortID 从 Reality 配置中提取 short-id，
// 兼容字符串、字符串数组、[]any 数组及数字等格式，
// 始终返回字符串（short-id 是十六进制字符串，数字形式无意义）
func extractShortID(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []string:
		if len(val) > 0 {
			return val[0]
		}
		return ""
	case []any:
		if len(val) > 0 {
			return fmt.Sprintf("%v", val[0])
		}
		return ""
	case nil:
		return ""
	default:
		// 数字等其他类型：强制转字符串，并记录日志便于排查
		s := fmt.Sprintf("%v", val)
		slog.Debug("extractShortID: 非预期类型，已转换为字符串",
			"type", fmt.Sprintf("%T", v), "value", s)
		return s
	}
}

func EnsureScheme(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		return s
	}

	if strings.HasPrefix(s, "127.0.0.1") || strings.HasPrefix(s, "localhost") {
		return "http://" + s
	}

	// Github 默认 HTTPS
	if strings.HasPrefix(s, "raw.githubusercontent.com/") || strings.HasPrefix(s, "github.com/") {
		return "https://" + s
	}

	// 本地环境默认 HTTP
	if utils.IsLocalURL(strings.Split(s, ":")[0]) {
		return "http://" + s
	}

	return "http://" + s
}

func SplitHostPortLoose(hp string) (string, string) {
	if hp == "" {
		return "", ""
	}
	if host, port, err := net.SplitHostPort(hp); err == nil {
		return host, port
	}
	// 回退逻辑：取最后一个冒号
	lastColon := strings.LastIndexByte(hp, ':')
	if lastColon > 0 && lastColon < len(hp)-1 {
		// 排除 IPv6 的情况 (冒号在方括号内)
		// 简单 heuristic: 如果有 ']' 且位置在冒号之后，那这个冒号可能不是端口分隔符
		// 但通常 [::1]:8080，LastIndexByte 找到的是最后一个冒号，肯定是端口
		// 唯独 [::1] 这种没有端口的纯 IPv6 需要注意
		if hp[len(hp)-1] == ']' {
			return hp, ""
		}
		return hp[:lastColon], hp[lastColon+1:]
	}
	return hp, ""
}

// guessSchemeByURL 根据 URL 文件名猜测协议
func guessSchemeByURL(raw string) string {
	// uParsed, err := url.Parse(raw)
	// if err != nil {
	// 	return "" // 解析失败，无法提取文件名，放弃猜测
	// }

	// filename := strings.ToLower(filepath.Base(uParsed.Path))
	pathStr := raw
	if idx := strings.Index(pathStr, "://"); idx >= 0 {
		pathStr = pathStr[idx+3:]
	}
	// 去掉 query/fragment
	if idx := strings.IndexAny(pathStr, "?#"); idx >= 0 {
		pathStr = pathStr[:idx]
	}
	// 获取 base
	filename := filepath.Base(pathStr)
	filename = strings.ToLower(filename)

	// 去掉扩展名
	if idx := strings.LastIndexByte(filename, '.'); idx > 0 {
		filename = filename[:idx]
	}

	// // 初始化排序后的 Key 列表 (仅执行一次)
	// sortedProtocolKeysOnce.Do(func() {
	// 	keys := lo.Keys(protocolSchemes)
	// 	// 只有不在 protocolSchemes 里的才需要加到 extras
	// 	extras := []string{"http2"}
	// 	keys = append(keys, extras...)
	// 	keys = lo.Uniq(keys)

	// 	// 按长度降序排序
	// 	sort.Slice(keys, func(i, j int) bool {
	// 		return len(keys[i]) > len(keys[j])
	// 	})
	// 	sortedProtocolKeys = keys
	// })

	// 直接使用手动排序后的列表
	for _, key := range sortedProtocolKeys {
		if strings.Contains(filename, key) {
			if _, ok := protocolSchemes[key]; ok {
				return key
			}
			if key == "http2" {
				return "https"
			}
		}
	}

	if strings.Contains(filename, "all") {
		return "all"
	}
	// 3. 如果文件名没有特征（比如 "nodes.txt"），返回空字符串意味着“未知协议”
	return ""
}

// TryDecodeBase64 尝试 Base64 解码，失败则返回原数据
func TryDecodeBase64(data []byte) []byte {
	// 清洗所有空白字符（空格、换行、回车、制表符）
	s := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1 // 丢弃字符
		}
		return r
	}, string(data))

	if len(s) == 0 {
		return data
	}

	var decoded []byte
	var err error

	// 根据长度进行分支预测解码
	if len(s)%4 == 0 {
		// 长度为 4 的倍数，说明包含 '=' 或者是完美的块，使用带 Padding 的标准解码器
		if decoded, err = base64.StdEncoding.DecodeString(s); err == nil {
			return decoded
		}
		if decoded, err = base64.URLEncoding.DecodeString(s); err == nil {
			return decoded
		}
	} else {
		// 长度非 4 的倍数，说明缺失了 '='，优先使用 Raw (无 Padding) 解码器
		if decoded, err = base64.RawStdEncoding.DecodeString(s); err == nil {
			return decoded
		}
		if decoded, err = base64.RawURLEncoding.DecodeString(s); err == nil {
			return decoded
		}

		// 兜底：针对某些被暴力截断、甚至连 Raw 解码器都无法识别的奇葩数据
		// 我们主动帮它补齐 '=' 再试一次
		pad := 4 - (len(s) % 4)
		padded := s + strings.Repeat("=", pad)
		if decoded, err = base64.StdEncoding.DecodeString(padded); err == nil {
			return decoded
		}
		if decoded, err = base64.URLEncoding.DecodeString(padded); err == nil {
			return decoded
		}
	}

	// 如果所有的努力都失败了，说明这可能本身就不是一个 base64 数据（比如普通纯文本 yaml）
	// 原样返回
	return data
}

// ToProxyNodes 将 Mihomo 的转换结果 []map[string]any 转换为 []ProxyNode 并进行标准化
func ToProxyNodes(list []map[string]any) []ProxyNode {
	if list == nil {
		return nil
	}
	res := make([]ProxyNode, len(list))
	for i, v := range list {
		// 立即进行标准化，防止后续处理遇到类型不一致问题
		NormalizeNode(v)
		res[i] = ProxyNode(v)
	}
	return res
}

// --------节点标准化与清洗--------

// NormalizeNode 统一清洗节点字段
// 将各种非标准或类型不确定的字段转换为 Clash/Mihomo 标准格式
func NormalizeNode(m map[string]any) {
	if m == nil {
		return
	}

	// 不一定需要转换
	if p, ok := m["port"]; ok {
		m["port"] = ToIntPort(p)
	}

	// Mihomo decoder 在处理非 bool 类型的布尔字段时可能 panic
	for _, field := range []string{
		"tls", "udp", "skip-cert-verify", "tfo",
		"allow-insecure", "xudp", "reuse-addr", "disable-sni",
	} {
		if val, ok := m[field]; ok {
			// 如果已经是 bool，跳过，避免不必要的写入
			if _, isBool := val.(bool); !isBool {
				m[field] = ToBool(val)
			}
		}
	}

	// 3. 协议类型：统一小写
	tObj, hasType := m["type"]
	if !hasType {
		return
	}
	t := strings.ToLower(fmt.Sprintf("%v", tObj))
	m["type"] = t

	// 4. 协议特定的必要修正
	switch t {
	case "https":
		// Mihomo 不认识 "https" type，转换为标准写法
		m["type"] = "http"
		m["tls"] = true
	case "trojan":
		// 来源数据经常漏 tls 字段，Trojan 协议本身强依赖 TLS
		if _, hasTLS := m["tls"]; !hasTLS {
			m["tls"] = true
		}

	case "http":
		// 443 端口的 http 节点大概率是 HTTPS，补充推断
		if _, hasTLS := m["tls"]; !hasTLS && ToIntPort(m["port"]) == 443 {
			m["tls"] = true
		}
	case "vmess", "vless":
		// V2Ray 格式用 security:"tls" 表达 TLS，Clash 格式用 tls:true
		if val, ok := m["security"].(string); ok && strings.EqualFold(val, "tls") {
			if _, hasTLS := m["tls"]; !hasTLS {
				m["tls"] = true
			}
		}
		// xhttp 网络的 path 必须在 xhttp-opts 内，不能放顶层
		if net, ok := m["network"].(string); ok && net == "xhttp" {
			xhttpOpts, _ := m["xhttp-opts"].(map[string]any)
			if xhttpOpts == nil {
				xhttpOpts = map[string]any{}
			}
			if _, hasPath := xhttpOpts["path"]; !hasPath {
				xhttpOpts["path"] = "/"
			}
			m["xhttp-opts"] = xhttpOpts
			// delete(m, "path") // FIXME: 验证是否应清理
		}

	case "hysteria2", "hy2":
		// 下划线字段名 → 连字符字段名
		if val, exists := m["obfs_password"]; exists {
			m["obfs-password"] = val
			delete(m, "obfs_password")
		}
	}

	// WS 扁平字段整合：ws-path / ws-headers → ws-opts
	normalizeWsFields(m)
}

func normalizeWsFields(m map[string]any) {
	// 只有当明确存在 key 时才进行后续 map 分配操作
	pathV, hasPath := m["ws-path"]
	headV, hasHead := m["ws-headers"]

	if !hasPath && !hasHead {
		return
	}

	if hasPath {
		delete(m, "ws-path")
	}
	if hasHead {
		delete(m, "ws-headers")
	}

	wsOpts, ok := m["ws-opts"].(map[string]any)
	if !ok {
		// 懒分配：仅在需要时创建 map
		wsOpts = make(map[string]any, 2)
	}

	if hasPath {
		wsOpts["path"] = pathV
	}
	if hasHead {
		wsOpts["headers"] = headV
	}

	m["ws-opts"] = wsOpts
	if _, ok := m["network"]; !ok {
		m["network"] = "ws"
	}
}

// FixupProxyLink 修复非标准链接头
func FixupProxyLink(link string) string {
	// 常见错误：hy:// 应为 hysteria://
	if len(link) > 4 {
		if strings.HasPrefix(link, "hy://") {
			return "hysteria://" + link[5:]
		}
		if strings.HasPrefix(link, "hy2://") {
			return "hysteria2://" + link[6:]
		}
	}
	return link
}

// ToIntPort 覆盖所有 Go 数值类型，兜底记录未知类型便于扩展排查
func ToIntPort(v any) int {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	// 有符号整数
	case int:
		return val
	case int8:
		return int(val)
	case int16:
		return int(val)
	case int32:
		return int(val)
	case int64:
		return int(val)
	// 无符号整数（go-yaml 常用 uint64）
	case uint:
		return int(val)
	case uint8:
		return int(val)
	case uint16:
		return int(val)
	case uint32:
		return int(val)
	case uint64:
		return int(val)
	// 浮点（JSON 默认 float64）
	case float32:
		return int(val)
	case float64:
		return int(val)
	// 字符串（如 "443" 或 "443.0"）
	case string:
		s := strings.TrimSpace(val)
		if i := strings.IndexByte(s, '.'); i > 0 {
			s = s[:i]
		}
		if p, err := strconv.Atoi(s); err == nil {
			return p
		}
		return 0
	default:
		// 兜底：转字符串解析，并记录类型信息便于未来扩展
		s := fmt.Sprintf("%v", v)
		if i := strings.IndexByte(s, '.'); i > 0 {
			s = s[:i]
		}
		if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			slog.Debug("ToIntPort: 兜底转换成功，建议添加显式 case",
				"type", fmt.Sprintf("%T", v), "value", v)
			return p
		}
		slog.Warn("ToIntPort: 无法转换端口，请检查数据来源",
			"type", fmt.Sprintf("%T", v), "value", v)
		return 0
	}
}

// ToBool 极其宽容的布尔值转换函数
func ToBool(v any) bool {
	if v == nil {
		return false
	}

	// 快速路径
	if b, ok := v.(bool); ok {
		return b
	}

	// 转字符串
	s := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))

	// 匹配常见为 true 的情况
	if s == "true" || s == "1" || s == "yes" || s == "on" {
		return true
	}
	return false
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// 辅助函数：快速检查字符串是否全为数字
func isDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
