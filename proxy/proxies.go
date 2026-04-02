// Package proxies 负责从各类订阅源获取、解析并去重代理节点。
package proxies

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/samber/lo"
	"github.com/sinspired/subs-check-pro/config"
	"github.com/sinspired/subs-check-pro/proxy/parse"
	"github.com/sinspired/subs-check-pro/save/method"
	"github.com/sinspired/subs-check-pro/utils"
)

type SubUrls struct {
	SubUrls []string `yaml:"sub-urls" json:"sub-urls"`
}

// SubStat 记录订阅链接的总数和成功数
type SubStat struct {
	Total   int
	Success int
}

var (
	ErrIgnore           = errors.New("error-ignore") // ErrIgnore 标记无需记录日志的非致命错误
	uniqueSubsCount int = 0                          // 去重后的订阅数量
	SubStats            = make(map[string]SubStat)   // SubStats 存储订阅总数和成功数
)

// logSubscriptionStats 打印订阅数量统计
func logSubscriptionStats(total, local, remote, history int) {
	args := []any{}
	if local > 0 {
		args = append(args, "本地", local)
	}
	if remote > 0 {
		args = append(args, "远程", remote)
	}
	if history > 0 {
		args = append(args, "历史", history)
	}
	if total < local+remote+history {
		args = append(args, "总计[去重]", total)
	} else {
		args = append(args, "总计", total)
	}

	uniqueSubsCount = total

	slog.Info("订阅数量", args...)

	if len(config.GlobalConfig.NodeType) > 0 {
		val := "[" + strings.Join(config.GlobalConfig.NodeType, ",") + "]"
		slog.Info("代理协议筛选", slog.String("Type", val))
	}

}

func logFatal(err error, urlStr string) {
	if code, convErr := strconv.Atoi(err.Error()); convErr == nil {
		// err 是数字字符串，按状态码处理
		var msg string
		switch code {
		case 400:
			msg = "\033[31m错误请求\033[0m"
		case 401, 403:
			msg = "\033[31m无权限访问\033[0m"
		case 404:
			msg = "\033[31m订阅失效\033[0m"
		case 405:
			msg = "方法不被允许"
		case 408:
			msg = "请求超时"
		case 410:
			msg = "\033[31m资源已永久删除\033[0m"
		case 429:
			msg = "\033[33m请求过多，被限流\033[0m"
		case 500, 502, 503, 504:
			msg = "\033[31m服务端/网关错误\033[0m"
		default:
			msg = "请求失败"
		}
		// 对失效订阅加上删除线效果
		if code == 404 || code == 401 || code == 410 {
			urlStr = "\033[9m" + urlStr + "\033[29m"
		}

		slog.Error(msg, "URL", urlStr, "status", code)

	} else {
		// 普通错误
		slog.Error("获取失败", "URL", urlStr, "error", err)
	}
}

// GetProxies 主入口：获取、解析、去重及统计代理节点
func GetProxies() ([]map[string]any, int, int, int, error) {
	// 每次进入先清空上次的连接池
	ClearCache()

	// 初始化代理环境变量
	initEnvironment()

	// 获取远程订阅列表
	subUrls, localNum, remoteNum, historyNum := resolveSubUrls()
	logSubscriptionStats(len(subUrls), localNum, remoteNum, historyNum)

	// 增大缓冲，减少消费者阻塞
	proxyChan := make(chan map[string]any, 100000)

	// 定义优先级常量
	const (
		KeepLevelNone    = 0 // 普通节点：无特殊保留策略
		KeepLevelHistory = 1 // 历史节点：多次成功或历史积累，价值优于普通
		KeepLevelSuccess = 2 // 成功节点：上次检测存活，价值最高，必须保留
	)

	var (
		wg sync.WaitGroup

		// 统计计数（原始数量）
		rawCount = 0

		// 存储去重后的节点。
		// Key 为节点指纹，Value 为节点数据。
		// 当指纹冲突时，保留优先级较高的版本 (Success > History > Normal)。
		uniqueNodes = make(map[string]map[string]any, 200000)

		// 记录已存储节点的优先级，用于比较
		nodeKeepLevels = make(map[string]int, 200000)
	)

	// GC 阈值，每10万个节点进行一次GC，避免内存无限上涨
	const gcInterval = 100000
	pendingGCNum := 0

	// 处理获取节点，消费 proxyChan
	done := make(chan struct{})
	go func() {
		defer close(done)

		for proxy := range proxyChan {
			rawCount++
			pendingGCNum++

			// 流式 GC 控制
			if pendingGCNum >= gcInterval {
				// 在 GC 导致停顿前记录日志
				slog.Debug("触发流式内存清理", "当前已处理总数", rawCount)
				// 归还内存
				// 否则百万级节点池内存将持续增加
				debug.FreeOSMemory()
				pendingGCNum = 0
			}

			// 1. 统计订阅源
			if su, ok := proxy["sub_url"].(string); ok && su != "" {
				stats := SubStats[su]
				stats.Total++
				SubStats[su] = stats
			}

			// 2. 计算当前节点的优先级
			currentKeepLevel := KeepLevelNone
			if proxy["sub_was_succeed"] == true {
				currentKeepLevel = KeepLevelSuccess
			} else if proxy["sub_from_history"] == true {
				currentKeepLevel = KeepLevelHistory
			}

			// 3. 生成指纹
			key := utils.GenerateProxyKey(proxy)

			// 4. 优先级竞争逻辑 (替代 DeduplicateAndMerge)
			if existLevel, exists := nodeKeepLevels[key]; exists {
				// 如果已存在，且新节点优先级更高，则覆盖（升级）
				if currentKeepLevel > existLevel {
					uniqueNodes[key] = proxy
					nodeKeepLevels[key] = currentKeepLevel
				}
				// 如果优先级相同或更低，直接丢弃（GC 会自动回收该 proxy）
			} else {
				// 如果不存在，直接存入
				uniqueNodes[key] = proxy
				nodeKeepLevels[key] = currentKeepLevel
			}
		}
	}()

	// 最低拉取并发数
	minCurrency := 50

	// 检测是否为 32 位系统
	// ^uint(0)>>32 在 32位系统上等于 0，在 64位系统上等于 1
	is32Bit := ^uint(0)>>32 == 0

	// 不管物理内存（RAM）有多大，32位程序能寻址的虚拟内存通常被限制在 2GB 到 3GB 之间。
	if is32Bit {
		minCurrency = min(10, config.GlobalConfig.Concurrent)
		slog.Warn("32 位程序强制保守拉取订阅", "并发", minCurrency)
		slog.Warn("建议使用 x64 位程序释放最佳性能！")

		// 激进的 GC 策略：内存增长 20% 就触发 GC（默认是 100%）
		debug.SetGCPercent(20)
	}

	// 获取订阅节点，生成proxyChan
	concurrency := min(config.GlobalConfig.Concurrent, minCurrency)
	sem := make(chan struct{}, concurrency)
	listenPort := strings.TrimPrefix(config.GlobalConfig.ListenPort, ":")
	subStorePort := strings.TrimPrefix(config.GlobalConfig.SubStorePort, ":")

	for _, subURL := range subUrls {
		wg.Add(1)
		sem <- struct{}{}
		isSucced, isHistory, tag := identifyLocalSubType(subURL, listenPort, subStorePort)
		go func(u, t string, succ, hist bool) {
			defer wg.Done()
			defer func() { <-sem }()
			processSubscription(u, t, succ, hist, proxyChan)
		}(subURL, tag, isSucced, isHistory)
	}

	wg.Wait()
	close(proxyChan)
	<-done

	// 将 Map 转为 Slice，并统计最终的分类数量
	finalProxies := make([]map[string]any, 0, len(uniqueNodes))
	finalSuccCount := 0
	finalHistCount := 0

	for key, node := range uniqueNodes {
		keepLevel := nodeKeepLevels[key]

		// 统计逻辑：根据最终留下的那个节点的优先级计数
		switch keepLevel {
		case KeepLevelSuccess:
			finalSuccCount++
		case KeepLevelHistory:
			finalHistCount++
		}

		// 清理元数据
		cleanMetadata(node)

		finalProxies = append(finalProxies, node)
	}

	// 打印去重统计日志
	slog.Info("节点解析",
		"原始", rawCount,
		"去重", len(finalProxies),
		"丢弃", rawCount-len(finalProxies),
	)
	saveStats(SubStats)

	// 释放 Map 内存（虽然函数返回后也会释放）
	uniqueNodes = nil
	nodeKeepLevels = nil

	// 归还内存
	debug.FreeOSMemory()

	return finalProxies, rawCount, finalSuccCount, finalHistCount, nil
}

// resolveSubUrls 合并本地与远程订阅清单并去重
func resolveSubUrls() ([]string, int, int, int) {
	var localNum, remoteNum, historyNum int
	localNum = len(config.GlobalConfig.SubUrls)

	urls := make([]string, 0, len(config.GlobalConfig.SubUrls))
	urls = append(urls, config.GlobalConfig.SubUrls...)

	if len(config.GlobalConfig.SubUrlsRemote) != 0 {
		slog.Info("获取远程订阅列表")
		for _, subURLRemote := range config.GlobalConfig.SubUrlsRemote {
			// 处理为标准的raw地址
			subURLRemote = parse.NormalizeGitHubRawURL(subURLRemote)
			warped := utils.WarpURL(subURLRemote, utils.IsGhProxyAvailable)
			if remote, err := fetchRemoteSubUrls(warped); err != nil {
				if !errors.Is(err, ErrIgnore) {
					logFatal(err, subURLRemote)
				}
			} else {
				remoteNum += len(remote)
				urls = append(urls, remote...)
			}
		}
	} else {
		slog.Info("获取订阅列表")
	}

	requiredListenPort := strings.TrimSpace(strings.TrimPrefix(config.GlobalConfig.ListenPort, ":"))
	localLastSucced := "http://127.0.0.1:" + requiredListenPort + "/all.yaml"
	localHistory := "http://127.0.0.1:" + requiredListenPort + "/history.yaml"

	// 如果用户设置了保留成功节点，则把本地的 all.yaml 和 history.yaml 放到最前面
	if config.GlobalConfig.KeepSuccessProxies {
		saver, err := method.NewLocalSaver()
		saver.OutputPath = filepath.Join(saver.OutputPath, "sub")
		if err == nil {
			if !filepath.IsAbs(saver.OutputPath) {
				saver.OutputPath = filepath.Join(saver.BasePath, saver.OutputPath)
			}
			localLastSuccedFile := filepath.Join(saver.OutputPath, "all.yaml")
			localHistoryFile := filepath.Join(saver.OutputPath, "history.yaml")

			if _, err := os.Stat(localLastSuccedFile); err == nil {
				historyNum++
				urls = append([]string{localLastSucced + "#Succeed"}, urls...)
			}
			if _, err := os.Stat(localHistoryFile); err == nil {
				historyNum++
				urls = append([]string{localHistory + "#History"}, urls...)
			}
		}
	}

	// 去重并过滤本地 URL（忽略 fragment）
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, s := range urls {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}

		key := s
		if d, err := url.Parse(s); err == nil {
			d.Fragment = ""
			key = d.String()

			// 如果不保留成功节点，过滤掉本地 all.yaml 和 history.yaml
			if !config.GlobalConfig.KeepSuccessProxies &&
				(key == localLastSucced || key == localHistory) {
				continue
			}
		}

		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out, localNum, remoteNum, historyNum
}

// fetchRemoteSubUrls 从远程地址读取订阅URL清单
func fetchRemoteSubUrls(listURL string) ([]string, error) {
	if listURL == "" {
		return nil, errors.New("远程列表为空")
	}
	data, err := FetchSubsData(listURL)
	if err != nil {
		return nil, err
	}

	// 1) 优先尝试解析为对象形式 (sub-urls: [...])
	var obj SubUrls
	if err := yaml.Unmarshal(data, &obj); err == nil && len(obj.SubUrls) > 0 {
		return obj.SubUrls, nil
	}

	// 2) 尝试解析为数组形式 ([...])
	var arr []string
	if err := yaml.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}

	// 2.5) 解析为通用 map，尝试从 Clash/Mihomo 配置中提取 proxy-providers.*.url
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err == nil && len(generic) > 0 {
		if urls := parse.ExtractClashProviderURLs(generic); len(urls) > 0 {
			return urls, nil
		}
	}

	// 3) 尝试从 Markdown 链接语法提取: [描述](https://...)
	if urls := parse.ExtractMarkdownURLs(data); len(urls) > 0 {
		slog.Debug("从 Markdown 链接提取订阅URL", "count", len(urls))
		return urls, nil
	}

	// 4) 回退为按行解析 (纯文本) + 快速 URL 校验
	res := make([]string, 0, 16)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "-"); ok {
			line = strings.TrimSpace(after)
		}
		line = strings.Trim(line, "\"'")

		// 必须显式包含协议，仅接受 http/https
		if parsed, perr := url.Parse(line); perr == nil {
			scheme := strings.ToLower(parsed.Scheme)
			if (scheme == "http" || scheme == "https") && parsed.Host != "" {
				res = append(res, line)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

// processSubscription 单个订阅的处理流程
func processSubscription(urlStr, tag string, wasSucced, wasHistory bool, out chan<- map[string]any) {
	// 1. 下载
	data, err := FetchSubsData(urlStr)
	if err != nil {
		if !errors.Is(err, ErrIgnore) {
			// 根据错误类型打印错误消息
			logFatal(err, urlStr)
		}
		return
	}

	// 2. 解析
	nodes, err := parse.ParseSubscriptionData(data, urlStr)
	if err != nil {
		// 回退策略：尝试正则暴力提取
		nodes = parse.FallbackExtractV2Ray(data, urlStr)
		data = nil //nolint:ineffassign
		if len(nodes) == 0 {
			if !hasDatePlaceholder(urlStr) {
				slog.Warn("解析失败或为空列表", "URL", urlStr, "error", err)
			}
			return
		}
	}

	slog.Debug("processSubscription", "nodes", nodes)

	data = nil //nolint:ineffassign

	// 3. 过滤与发送
	count := 0
	filterTypes := config.GlobalConfig.NodeType

	for _, node := range nodes {
		slog.Debug("解析代理节点成功", "node", node)
		// 类型过滤
		if len(filterTypes) > 0 {
			if t, ok := node["type"].(string); ok && !lo.Contains(filterTypes, t) {
				continue
			}
		}

		slog.Debug("processSubscription", "node", node)

		// 统一清洗节点字段，注入默认值
		parse.NormalizeNode(node)

		// 最终验证
		serverStr := strings.TrimSpace(fmt.Sprintf("%v", node["server"]))
		port := parse.ToIntPort(node["port"])
		if serverStr == "" || serverStr == "<nil>" || port <= 0 || port > 65535 || node["type"] == nil {
			slog.Debug("过滤掉无效的畸形节点", "订阅", urlStr, "数据", node)
			continue
		}

		slog.Debug("processSubscription", "NormalizeNode", node)

		node["sub_url"] = urlStr
		node["sub_tag"] = tag
		node["sub_was_succeed"] = wasSucced
		node["sub_from_history"] = wasHistory

		out <- node
		count++
	}

	slog.Debug("订阅解析完成", "URL", urlStr, "有效节点", count)
}

// identifyLocalSubType 识别本地订阅源类型
func identifyLocalSubType(subURL, listenPort, storePort string) (isLatest, isHistory bool, tag string) {
	u, err := url.Parse(subURL)
	if err != nil {
		return false, false, ""
	}

	tag = u.Fragment
	port := u.Port()

	// 必须是本地地址
	if !utils.IsLocalURL(subURL) {
		return false, false, tag
	}

	// 端口必须匹配当前服务端口或存储端口
	if port != listenPort && port != storePort {
		return false, false, tag
	}

	// 路径分类
	path := u.Path
	isLatest = strings.HasSuffix(path, "/all.yaml") || strings.HasSuffix(path, "/all.yml")
	isHistory = strings.HasSuffix(path, "/history.yaml") || strings.HasSuffix(path, "/history.yml")

	return isLatest, isHistory, tag
}

// saveStats 保存统计信息
func saveStats(subStats map[string]SubStat) {
	// 构造 pair 列表
	type pair struct {
		URL     string
		Total   int
		Success int
	}
	pairs := make([]pair, 0, len(subStats))
	for u, st := range subStats {
		pairs = append(pairs, pair{u, st.Total, st.Success})
	}

	// 按总数降序，再按 URL 升序
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Total == pairs[j].Total {
			return pairs[i].URL < pairs[j].URL
		}
		return pairs[i].Total > pairs[j].Total
	})

	var validSB strings.Builder
	validSB.WriteString("# 可直接替换 config.yaml 中的 subs-urls 字段\n")
	validSB.WriteString("sub-urls:\n")
	for _, p := range pairs {
		fmt.Fprintf(&validSB, "  - %q # nodes: %d\n", p.URL, p.Total)
	}

	if len(subStats) < uniqueSubsCount {
		validSB.WriteString("\n# 已剔除以下失效订阅链接：\n")
		for _, u := range config.GlobalConfig.SubUrls {
			if _, ok := subStats[u]; !ok {
				fmt.Fprintf(&validSB, "# - %q\n", u)
			}
		}
		_ = method.SaveToStats([]byte(validSB.String()), "sub-urls.yaml", "订阅净化")
	} else {
		validSB.WriteString("\n# 所有订阅链接均可用，已按照节点数量排序\n")
		_ = method.SaveToStats([]byte(validSB.String()), "sub-urls.yaml", "订阅排序")
	}

}

func cleanMetadata(p map[string]any) {
	delete(p, "sub_was_succeed")
	delete(p, "sub_from_history")
}

// ClearCache 检测结束后释放包级全局状态
func ClearCache() {
	uniqueSubsCount = 0

	// 关闭所有复用 client 的连接池，释放 TLS session cache 和 idle conn
	clientMapCache.Range(func(key, value any) bool {
		if c, ok := value.(*http.Client); ok {
			c.CloseIdleConnections()
		}
		clientMapCache.Delete(key)
		return true
	})
}
