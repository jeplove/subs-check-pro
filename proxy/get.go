package proxies

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
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
	"time"

	"github.com/goccy/go-yaml"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/samber/lo"
	"github.com/sinspired/subs-check-pro/config"
	"github.com/sinspired/subs-check-pro/save/method"
	"github.com/sinspired/subs-check-pro/utils"
)

// ProxyNode 定义通用节点结构类型
type ProxyNode map[string]any

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

// initEnvironment 初始化代理环境变量
func initEnvironment() {
	saver, err := method.NewLocalSaver()
	if err == nil {
		srcDir := saver.OutputPath
		targetDir := filepath.Join(saver.OutputPath, "sub")
		saver.OutputPath = targetDir
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			slog.Error("创建 sub 目录失败", "error", err)
		}
		if err := migrateOldFiles(srcDir, "history.yaml", targetDir); err == nil {
			os.Remove(filepath.Join(srcDir, "history.yaml"))
		} else {
			slog.Info("迁移出错", "error", err, "srcDir", srcDir, "targetDir", targetDir)
		}
		if err := migrateOldFiles(srcDir, "all.yaml", targetDir); err == nil {
			os.Remove(filepath.Join(srcDir, "all.yaml"))
		}
		if err := migrateOldFiles(srcDir, "mihomo.yaml", targetDir); err == nil {
			os.Remove(filepath.Join(srcDir, "mihomo.yaml"))
		}
		if err := migrateOldFiles(srcDir, "base64.txt", targetDir); err == nil {
			os.Remove(filepath.Join(srcDir, "base64.txt"))
		}
	}

	slog.Info("获取系统代理和Github代理状态")
	utils.IsSysProxyAvailable = utils.GetSysProxy()
	utils.IsGhProxyAvailable = utils.GetGhProxy()
	if utils.IsSysProxyAvailable {
		slog.Info("", "-system-proxy", config.GlobalConfig.SystemProxy)
	}
	if utils.IsGhProxyAvailable {
		slog.Info("", "-github-proxy", config.GlobalConfig.GithubProxy)
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
	proxyChan := make(chan ProxyNode, 100000)

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
		uniqueNodes = make(map[string]ProxyNode, 200000)

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
			key := GenerateProxyKey(proxy)

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

		// 这里的显式转换是为了满足返回值类型 []map[string]any
		finalProxies = append(finalProxies, map[string]any(node))
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
			subURLRemote = NormalizeGitHubRawURL(subURLRemote)
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
		if urls := extractClashProviderURLs(generic); len(urls) > 0 {
			return urls, nil
		}
	}

	// 3) 回退为按行解析 (纯文本) + 快速 URL 校验
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
func processSubscription(urlStr, tag string, wasSucced, wasHistory bool, out chan<- ProxyNode) {
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
	nodes, err := parseSubscriptionData(data, urlStr)
	if err != nil {
		// 回退策略：尝试正则暴力提取
		nodes = fallbackExtractV2Ray(data, urlStr)
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
		NormalizeNode(node)

		// 最终验证
		serverStr := strings.TrimSpace(fmt.Sprintf("%v", node["server"]))
		port := ToIntPort(node["port"])
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

// fallbackExtractV2Ray 正则提取兜底
func fallbackExtractV2Ray(data []byte, subURL string) []ProxyNode {
	decodedData := TryDecodeBase64(data)
	slog.Debug("base64解码", "decode", string(decodedData))
	links := ExtractV2RayLinks(decodedData)
	if len(links) == 0 {
		return nil
	}
	slog.Debug("正则提取链接", "数量", len(links), "URL", subURL)

	return ParseProxyLinksAndConvert(links, subURL)
}

// FetchSubsData 获取数据 (包含重试、占位符处理、代理策略)
func FetchSubsData(rawURL string) ([]byte, error) {
	// 清洗 URL
	rawURL = CleanURL(rawURL)

	if _, err := url.Parse(rawURL); err != nil {
		return nil, err
	}

	slog.Debug("正在下载订阅", "URL", rawURL)

	conf := config.GlobalConfig
	maxRetries := max(1, conf.SubUrlsReTry)
	timeout := max(10, conf.SubUrlsTimeout)

	// 处理为标准的GitHub raw地址
	rawURL = NormalizeGitHubRawURL(rawURL)

	candidates, hasPlaceholder := buildCandidateURLs(rawURL)
	var lastErr error

	// 定义请求策略
	type strategy struct {
		useProxy bool
		urlFunc  func(string) string
	}

	strategies := []strategy{}

	warpFunc := func(s string) string { return utils.WarpURL(EnsureScheme(s), true) }
	originFunc := func(s string) string { return EnsureScheme(s) }

	if utils.IsLocalURL(rawURL) {
		strategies = append(strategies, strategy{false, warpFunc})
	} else {
		// 1. 系统代理 (External utils)
		if utils.IsSysProxyAvailable {
			strategies = append(strategies, strategy{true, originFunc})
		}
		// 2. Github 代理 (External utils)
		if utils.IsGhProxyAvailable {
			strategies = append(strategies, strategy{false, warpFunc})
		}
		// 3. 直连兜底
		strategies = append(strategies, strategy{false, originFunc})
	}

	// UA 列表池
	uaList := []string{
		convert.RandUserAgent(),
		"mihomo/1.18.3",
		"clash.meta",
		"curl/8.16.0",
	}

	for i := range maxRetries {
		ua := uaList[i%len(uaList)]
		if i > 0 {
			time.Sleep(time.Duration(max(1, conf.SubUrlsRetryInterval)) * time.Second)
		}

		for _, candidate := range candidates {
			triedInThisLoop := make(map[string]struct{})

			for _, strat := range strategies {
				targetURL := strat.urlFunc(candidate)

				key := targetURL + "|" + strconv.FormatBool(strat.useProxy)

				if _, tried := triedInThisLoop[key]; tried {
					continue
				}
				triedInThisLoop[key] = struct{}{}

				// 保持 Debug，过于频繁的尝试详情不需要 Info
				slog.Debug("尝试下载", "Target", targetURL, "Proxy", strat.useProxy)

				body, err, fatal := fetchOnce(targetURL, strat.useProxy, timeout, ua)
				if err == nil {
					return body, nil
				}
				lastErr = err

				if fatal && !hasPlaceholder {
					return nil, err
				}
			}
		}
		if hasPlaceholder {
			return nil, ErrIgnore
		}
	}

	return nil, fmt.Errorf("%d次重试后失败: %v", maxRetries, lastErr)
}

// clientMap 用于缓存不同代理策略的 HTTP Client
// key: "direct" 或 proxyUrl (e.g. "http://127.0.0.1:7890")
// clientMapCache 使用 sync.Map 存储复用的 http.Client
// Key: proxyAddr (string), Value: *http.Client
var clientMapCache sync.Map

// getClient 根据代理地址获取复用的 Client
func getClient(proxyAddr string) *http.Client {
	if v, ok := clientMapCache.Load(proxyAddr); ok {
		return v.(*http.Client)
	}

	// 创建新的 Transport
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: false},
		MaxIdleConns:        100,              // 全局最大空闲连接
		MaxIdleConnsPerHost: 20,               // 每个 Host 最大空闲连接
		IdleConnTimeout:     90 * time.Second, // 空闲超时
		DisableKeepAlives:   false,            // 开启长连接复用
	}

	// 设置代理
	if proxyAddr != "direct" {
		if u, err := url.Parse(proxyAddr); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	} else {
		transport.Proxy = nil
	}

	// 创建 Client
	// timeout := max(10, config.GlobalConfig.SubUrlsTimeout)
	// 设置一个较大的超时，以在调用时控制超时
	newClient := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}

	// LoadOrStore 保证并发安全：如果其他协程已经创建了，就用它的，否则用我的
	actual, _ := clientMapCache.LoadOrStore(proxyAddr, newClient)
	return actual.(*http.Client)
}

// fetchOnce 执行单次 HTTP 请求 (使用连接池)
func fetchOnce(target string, useProxy bool, timeoutSec int, ua string) ([]byte, error, bool) {
	// 1. 确定 Client Key
	proxyKey := "direct"
	if useProxy {
		if p := config.GlobalConfig.SystemProxy; p != "" {
			proxyKey = p // 使用代理地址作为 Key
		}
	}

	// 2. 获取复用的 Client
	client := getClient(proxyKey)

	// 3. 创建带超时的连接
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)

	defer cancel()

	// 4. 创建请求
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err, false
	}
	if len(ua) <= 1 {
		ua = convert.RandUserAgent()
	}
	req.Header.Set("User-Agent", ua)

	// 4. 处理本地请求特殊 Header
	if isLocalRequest(req.URL) {
		req.Header.Set("X-From-Subs-Check-pro", "true")
		req.Header.Set("X-API-Key", config.GlobalConfig.APIKey)
		q := req.URL.Query()
		q.Set("from_subs_check", "true")
		req.URL.RawQuery = q.Encode()
	}

	// 5. 执行请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, err, false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// 读取128KB，超过的放弃连接复用
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 128*1024))
		slog.Debug("错误", "url", req.URL, "代理", useProxy, "状态码", resp.StatusCode, "UA", req.UserAgent())
		fatal := resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 410
		return nil, fmt.Errorf("%d", resp.StatusCode), fatal
	}

	// 限制最大读取 100MB
	const MaxLimit = 100 * 1024 * 1024

	// 如果 Content-Length 存在且超过限制，直接报错，避免无谓的读取
	if resp.ContentLength > MaxLimit {
		return nil, fmt.Errorf("订阅文件过大: %d MB", resp.ContentLength/1024/1024), true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxLimit))
	if err != nil {
		return nil, err, false
	}

	if len(body) >= MaxLimit {
		return nil, fmt.Errorf("订阅文件超过 50MB 限制"), true
	}

	return body, nil, false
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
