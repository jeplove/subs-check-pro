package parse

import (
	"fmt"
	"github.com/goccy/go-json"
	"log/slog"
	"strconv"
	"strings"
)

// NormalizeNode 统一清洗节点字段并验证节点合法性
// 将各种非标准或类型不确定的字段转换为 Clash/Mihomo 标准格式
// 返回 bool 表示该节点是否为有效节点（有效返回 true，无效应当丢弃）
func NormalizeNode(m map[string]any) bool {
	if m == nil {
		return false
	}

	// 1. 端口转换
	if p, ok := m["port"]; ok {
		m["port"] = ToIntPort(p)
	}

	// 2. Mihomo decoder 在处理非 bool 类型的布尔字段时可能 panic
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
		return false // 连 type 都没有的节点绝对是无效的
	}
	t := strings.ToLower(fmt.Sprintf("%v", tObj))
	m["type"] = t

	// 4. 协议特定的必要修正
	switch t {
	case "https":
		// Mihomo 不认识 "https" type，转换为标准写法
		m["type"] = "http"
		m["tls"] = true
		t = "http" // 同步更新 t，方便后面的终极校验
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
		}

	case "hysteria2", "hy2":
		// 下划线字段名 → 连字符字段名
		if val, exists := m["obfs_password"]; exists {
			m["obfs-password"] = val
			delete(m, "obfs_password")
		}

	// WireGuard 致命参数拦截
	case "wireguard", "wg":
		pk, _ := m["private-key"].(string)
		pubk, _ := m["public-key"].(string)

		if pk == "" || pubk == "" {
			m["type"] = "invalid"
			t = "invalid"
			slog.Debug("发现缺失公钥或私钥的畸形 WireGuard 节点，已拦截", "name", m["name"])
		}
	}

	// WS 扁平字段整合：ws-path / ws-headers → ws-opts
	normalizeWsFields(m)

	// 5. 终极有效性校验
	if t == "" || t == "invalid" {
		return false
	}

	server := strings.TrimSpace(fmt.Sprintf("%v", m["server"]))
	port := ToIntPort(m["port"])

	// 豁免本地和特殊策略类型（direct, reject, dns 等）
	if t != "direct" && t != "reject" && t != "dns" {
		if server == "" || server == "<nil>" || port <= 0 || port > 65535 {
			return false
		}
	}

	return true
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
		// 很多 JSON 解析器开启 UseNumber 选项时会产生该类型
	case json.Number:
		if p, err := val.Int64(); err == nil {
			return int(p)
		}
		return 0
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
