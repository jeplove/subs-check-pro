package proxies

import (
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
)

type ShuffleConfig struct {
	Threshold  float64    // 相邻相似度阈值，IPv4 /24 ≈ 0.75
	Passes     int        // 改善轮数（1~3）
	MinSpacing int        // 同一 IPv4 /24 的最小间距；<=0 关闭
	ScanLimit  int        // 冲突向前扫描的最大距离
	Rand       *rand.Rand // 随机数，为空则使用 time.Now().UnixNano()
}

type serverMeta struct {
	raw string

	// IPv4 相关
	isIPv4   bool
	octets   [4]byte
	prefix24 uint32

	// 域名相关
	isDomain    bool
	domainParts []string // 按 "." 分割后的域名片段
	rootDomain  string   // 提取的主域名 (等同于 IPv4 的 /24 网段概念)
}

// GroupKey 返回用于 MinSpacing 的唯一标识
// 如果返回空字符串，则代表不参与间距限制
func (m serverMeta) GroupKey() string {
	if m.isIPv4 {
		return "ip4:" + strconv.FormatUint(uint64(m.prefix24), 10)
	}
	if m.isDomain && m.rootDomain != "" {
		return "dom:" + m.rootDomain
	}
	return ""
}

// SmartShuffleByServer 对 items 就地打乱，避免相邻相似，并尽量满足最小间距
func SmartShuffleByServer(items []map[string]any, cfg ShuffleConfig) {
	n := len(items)
	if n < 2 {
		return
	}

	// 默认参数
	if cfg.Passes <= 0 {
		cfg.Passes = 2
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = 0.75
	}
	if cfg.ScanLimit <= 0 {
		cfg.ScanLimit = 64
	}

	// 预解析服务器元数据
	metas := make([]serverMeta, n)
	for i := range items {
		// 优先提取 CDN / SNI 节点可能存在的真实 Host
		serverName := ""
		if wsOpts, ok := items[i]["ws-opts"].(map[string]any); ok {
			if headers, ok := wsOpts["headers"].(map[string]any); ok {
				if host, ok := headers["Host"].(string); ok && host != "" {
					serverName = host
				}
			}
		}
		if serverName == "" {
			if sni, ok := items[i]["servername"].(string); ok && sni != "" {
				serverName = sni
			}
		}
		// 兜底使用 server 字段
		if serverName == "" {
			if s, ok := items[i]["server"].(string); ok && s != "" {
				serverName = s
			}
		}

		metas[i] = parseServerMeta(serverName)
	}

	// 初次完全打乱 (同时打乱 items 和 metas)
	rand.Shuffle(n, func(i, j int) {
		swap(items, metas, i, j)
	})

	// 检查最小间距的闭包函数
	checkSpacing := func(lp map[string]int, idx int, m serverMeta) bool {
		if cfg.MinSpacing <= 0 {
			return true
		}
		key := m.GroupKey()
		if key == "" {
			return true // 无法识别分组，不限制间距
		}
		// idx 是放置候选节点的位置，last 是上一次出现该 IP段/主域名 的位置
		// 要求: 当前位置 - 上次位置 > 最小间距
		if last, ok := lp[key]; !ok || idx-last > cfg.MinSpacing {
			return true
		}
		return false
	}

	for pass := 0; pass < cfg.Passes; pass++ {
		changed := false
		// 每次 pass 重置 lastPos map，记录各 GroupKey 最近一次出现的位置
		lastPos := make(map[string]int, n)

		// 记录第 0 个元素的位置
		if key := metas[0].GroupKey(); key != "" {
			lastPos[key] = 0
		}

		for i := 0; i < n-1; i++ {
			m1, m2 := metas[i], metas[i+1]

			// 检查 items[i] 和 items[i+1] 是否冲突
			conflict := similarity(m1, m2) >= cfg.Threshold ||
				(cfg.MinSpacing > 0 && sameGroup(m1, m2))

			if conflict {
				bestJ, bestScore := -1, 2.0 // 2.0 大于任何可能的相似度(最大1.0)

				// 向后搜索合适的候选者 j 来替换 i+1
				searchEnd := min(i+2+cfg.ScanLimit, n)

				for j := i + 2; j < searchEnd; j++ {
					mj := metas[j]

					// 候选者 mj 放到 i+1 的位置，必须满足与前面所有元素的间距要求
					if !checkSpacing(lastPos, i+1, mj) {
						continue
					}

					score := similarity(m1, mj)

					// 如果找到一个足够好的，直接交换并跳出
					if score < cfg.Threshold {
						swap(items, metas, i+1, j)
						m2 = mj // 更新 m2 为新节点，用于下一轮判断
						changed = true
						break
					}

					// 否则记录当前找到的相对最好的
					if score < bestScore {
						bestScore, bestJ = score, j
					}
				}

				// 如果没找到完美的，但找到了相对较好的，且满足间距要求，则替换
				if !changed && bestJ != -1 {
					if checkSpacing(lastPos, i+1, metas[bestJ]) {
						swap(items, metas, i+1, bestJ)
						changed = true
						m2 = metas[i+1]
					}
				}
			}

			// 更新 lastPos：现在 i+1 位置的元素已经确定
			if key := m2.GroupKey(); key != "" {
				lastPos[key] = i + 1
			}
		}

		if !changed {
			break
		}
	}
}

func parseServerMeta(s string) serverMeta {
	m := serverMeta{raw: s}
	if s == "" {
		return m
	}

	// 1. 检查是否为 IP (含 IPv4 和 IPv6)
	if ip := net.ParseIP(s); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			m.isIPv4 = true
			copy(m.octets[:], ip4)
			// 计算前24位
			m.prefix24 = uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8
		}
		return m // 即使是 IPv6 也直接返回，目前 IPv6 不计算相似度(通常差异极大)
	}

	// 2. 作为域名处理
	m.isDomain = true
	s = strings.ToLower(s)
	// 移除可能带的端口号
	if idx := strings.Index(s, ":"); idx != -1 {
		s = s[:idx]
	}

	m.domainParts = strings.Split(s, ".")
	m.rootDomain = extractRootDomain(m.domainParts)

	return m
}

// extractRootDomain 简单的启发式主域名提取 (不依赖复杂的 public suffix 库)
func extractRootDomain(parts []string) string {
	n := len(parts)
	if n == 0 {
		return ""
	}
	if n <= 2 {
		return strings.Join(parts, ".") // 例如 example.com
	}

	// 处理类似 .com.cn, .co.uk 的情况 (提取 3 段)
	sld := parts[n-2]
	switch sld {
	case "com", "co", "org", "net", "edu", "gov", "ac":
		return strings.Join(parts[n-3:], ".") // 例: www.google.com.cn -> google.com.cn
	default:
		return strings.Join(parts[n-2:], ".") // 例: us1.example.com -> example.com
	}
}

// sameGroup 判断两个节点是否属于同一个 IP /24 网段 或 同一个主域名
func sameGroup(a, b serverMeta) bool {
	keyA, keyB := a.GroupKey(), b.GroupKey()
	return keyA != "" && keyB != "" && keyA == keyB
}

// similarity 计算两个节点的相似度 (0.0 ~ 1.0)
func similarity(a, b serverMeta) float64 {
	// IPv4 相似度：从左向右对比 octets (段数)
	if a.isIPv4 && b.isIPv4 {
		eq := 0
		for i := range 4 {
			if a.octets[i] == b.octets[i] {
				eq++
			} else {
				break
			}
		}
		return float64(eq) / 4.0
	}

	// 域名相似度：从右向左对比层级 (例如 a.b.com 和 c.b.com 有 2 段相同)
	if a.isDomain && b.isDomain {
		pa, pb := a.domainParts, b.domainParts
		na, nb := len(pa), len(pb)
		if na == 0 || nb == 0 {
			return 0
		}
		match := 0
		// 从末尾（顶级域名）开始倒序比较
		for i, j := na-1, nb-1; i >= 0 && j >= 0; i, j = i-1, j-1 {
			if pa[i] == pb[j] {
				match++
			} else {
				break
			}
		}
		maxLen := max(na, nb)
		return float64(match) / float64(maxLen)
	}

	// 类型不同（例如一个 IP，一个域名）相似度直接为 0
	return 0
}

func swap(items []map[string]any, metas []serverMeta, i, j int) {
	items[i], items[j] = items[j], items[i]
	metas[i], metas[j] = metas[j], metas[i]
}

// ThresholdToLevel 根据 Threshold 动态返回对应的 (IP网段描述, 域名层级描述)
func ThresholdToLevel(th float64) (string, string) {
	switch th {
	case 1.0:
		return "/32", "域名"
	case 0.75:
		return "/24", "主域名"
	case 0.5:
		return "/16", "二级域"
	case 0.25:
		return "/8", "顶级域"
	default:
		// 兜底逻辑
		prefix := int(th*4) * 8
		if prefix <= 0 {
			prefix = 24
		} else if prefix > 32 {
			prefix = 32
		}
		return "/" + strconv.Itoa(prefix), "相似域"
	}
}
