package proxies

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"time"

	"github.com/sinspired/subs-check-pro/config"
	"github.com/sinspired/subs-check-pro/utils"
)

// 日期占位符正则表达式
var (
	dateRegexes = []struct {
		re     *regexp.Regexp
		format string
	}{
		{regexp.MustCompile(`(?i)\{ymd\}`), "20060102"},
		{regexp.MustCompile(`(?i)\{y-m-d\}`), "2006-01-02"},
		{regexp.MustCompile(`(?i)\{y_m_d\}`), "2006_01_02"},
		{regexp.MustCompile(`(?i)\{yy\}`), "2006"},
		{regexp.MustCompile(`(?i)\{y\}`), "2006"},
		{regexp.MustCompile(`(?i)\{mm\}`), "01"},
		{regexp.MustCompile(`(?i)\{m\}`), "1"},
		{regexp.MustCompile(`(?i)\{dd\}`), "02"},
		{regexp.MustCompile(`(?i)\{d\}`), "2"},
	}
)

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

// migrateOldFiles 迁移旧文件
func migrateOldFiles(srcDir, fileName, targetDir string) error {
	src := filepath.Join(srcDir, fileName)
	dst := filepath.Join(targetDir, fileName)

	// 目标已存在 -> 不做任何操作
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查目标文件失败: %w", err)
	}

	// 源不存在 -> 不做任何操作
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("检查源文件失败: %w", err)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("读取源文件失败: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("写入目标文件失败: %w", err)
	}
	return nil
}

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

func isLocalRequest(u *url.URL) bool {
	return utils.IsLocalURL(u.Hostname()) &&
		(strings.Contains(u.Fragment, "Keep") || strings.Contains(u.Path, "history") || strings.Contains(u.Path, "all"))
}

// NormalizeGitHubRawURL 将 GitHub 的 blob 或 raw 页面链接转换为 raw.githubusercontent.com 直链
func NormalizeGitHubRawURL(urlStr string) string {
	// 如果不是 github.com 的链接，或者已经是 raw.githubusercontent.com，直接返回
	if !strings.Contains(urlStr, "github.com") || strings.Contains(urlStr, "raw.githubusercontent.com") {
		return urlStr
	}

	// 移除可能存在的 www. 前缀，统一处理
	urlStr = strings.Replace(urlStr, "www.github.com", "github.com", 1)

	// 检查是否包含 /blob/ 或 /raw/
	// GitHub 结构通常是: github.com/{user}/{repo}/[blob|raw]/{branch}/{path}
	// 目标结构是: raw.githubusercontent.com/{user}/{repo}/{branch}/{path}

	if strings.Contains(urlStr, "/blob/") {
		urlStr = strings.Replace(urlStr, "github.com", "raw.githubusercontent.com", 1)
		urlStr = strings.Replace(urlStr, "/blob/", "/", 1)
	} else if strings.Contains(urlStr, "/raw/") {
		urlStr = strings.Replace(urlStr, "github.com", "raw.githubusercontent.com", 1)
		urlStr = strings.Replace(urlStr, "/raw/", "/", 1)
	}

	return urlStr
}

// buildCandidateURLs 生成候选链接
func buildCandidateURLs(u string) ([]string, bool) {
	if !hasDatePlaceholder(u) {
		return []string{u}, false
	}
	now := time.Now()
	yest := now.AddDate(0, 0, -1)
	today := replaceDatePlaceholders(u, now)
	yesterday := replaceDatePlaceholders(u, yest)
	slog.Debug("检测到日期占位符，将尝试今日和昨日日期")
	return []string{today, yesterday}, true
}

func hasDatePlaceholder(s string) bool {
	ls := strings.ToLower(s)
	return strings.Contains(ls, "{ymd}") || strings.Contains(ls, "{y}") ||
		strings.Contains(ls, "{m}") || strings.Contains(ls, "{mm}") ||
		strings.Contains(ls, "{d}") || strings.Contains(ls, "{dd}") ||
		strings.Contains(ls, "{y-m-d}") || strings.Contains(ls, "{y_m_d}")
}

func replaceDatePlaceholders(s string, t time.Time) string {
	out := s
	for _, item := range dateRegexes {
		// 只有当字符串包含 { 时才执行正则，提升极大性能
		if strings.Contains(out, "{") {
			out = item.re.ReplaceAllString(out, t.Format(item.format))
		}
	}
	return out
}

// CleanURL 清洗 URL，移除首尾空白及尾部常见的误复制标点符号
func CleanURL(raw string) string {
	// 1. 去除首尾的标准空白符 (空格, 换行, Tab)
	s := strings.TrimSpace(raw)

	// 2. 定义尾部需要剔除的“垃圾字符”集合
	// "  : 双引号
	// '  : 单引号
	// `  : 反引号 (Markdown常用)
	// ,  : 逗号
	// ;  : 分号
	// .  : 句号 (虽然URL允许结尾有点，但在订阅链接场景下通常是句尾误复制)
	// )  : 右括号 (Markdown链接常用)
	// ]  : 右方括号
	// }  : 右大括号
	// >  : 大于号 (Email/引用常用)
	cutset := "\"'`,;.)]}>"

	// 3. 循环移除尾部所有属于 cutset 的字符，直到遇到非 cutset 字符为止
	return strings.TrimRight(s, cutset)
}

func cleanMetadata(p ProxyNode) {
	delete(p, "sub_was_succeed")
	delete(p, "sub_from_history")
}
