package proxies

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/metacubex/mihomo/common/convert"
	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/sinspired/checkip/pkg/ipinfo"
	"github.com/sinspired/subs-check-pro/v2/config"
)

var ipAPIs = []string{
	// 可尝试在api后加 /cdn-cgi/trace, 如能返回loc则不可使用.
	"https://check.torproject.org/api/ip",
	"https://qifu-api.baidubce.com/ip/local/geo/v1/district",
	"https://r.inews.qq.com/api/ip2city",
	"https://g3.letv.com/r?format=1",
	"https://cdid.c-ctrip.com/model-poc2/h",
	"https://whois.pconline.com.cn/ipJson.jsp",
	"https://api.live.bilibili.com/xlive/web-room/v1/index/getIpInfo",
	"https://6.ipw.cn/",                  // IPv4使用了 CFCDN, IPv6 位置准确
	"https://api6.ipify.org?format=json", // IPv4使用了 CFCDN, IPv6 位置准确
}

var geoAPIs = []string{
	"https://ip.122911.xyz/api/ipinfo",
	"https://ident.me/json",
	"https://tnedi.me/json",
	"https://api.seeip.org/geoip",
}

var ipAPIsMe = []string{}

var geoAPIsMe = []string{
	"https://ip.122911.xyz/api/ipinfo",
}

// NewIPInfoClient 创建 ipinfo 检测客户端
func NewIPInfoClient(httpClient *http.Client, db *maxminddb.Reader, ipList, geoList []string) (*ipinfo.Client, error) {
	// 从当前项目 (subs-check-pro) 的全局配置中读取 ISP 设置
	c := config.GlobalConfig

	if c.ISPTimeout <= 0 {
		c.ISPTimeout = 5 // 默认 5 秒，最高 15 秒
	} else if c.ISPTimeout > 15 {
		c.ISPTimeout = 15 // 最大 15 秒
	}

	// 映射为 ipinfo 工具包所需的配置结构
	ispCfg := ipinfo.ISPConfig{
		ISPCheck:                 c.ISPCheck,
		ISPTimeout:               time.Duration(c.ISPTimeout) * time.Second,
		ISPCheckAPIKeyIPAPI:      c.ISPCheckAPIKeyIPAPI,
		ISPCheckAPIKeyProxyCheck: c.ISPCheckAPIKeyProxyCheck,
		ISPCheckAPIKeyIPLocate:   c.ISPCheckAPIKeyIPLocate,
		ISPCheckAPIKeyIPData:     c.ISPCheckAPIKeyIPData,
	}

	// 初始化客户端并注入配置
	return ipinfo.New(
		ipinfo.WithHTTPClient(httpClient),
		ipinfo.WithDBReader(db),
		ipinfo.WithIPAPIs(ipList...),
		ipinfo.WithGeoAPIs(geoList...),
		ipinfo.WithISPConfig(ispCfg),
	)
}

// GetProxyCountry 获取代理节点的位置代码，使用 github.com/sinspired/checkip/pkg/ipinfo API 获取 Analyzed 结果：
//
// - BadCFNode: HK⁻¹
//
// - CFNodeWithSameCountry: HK¹⁺
//
// - CFNodeWithDifferentCountry: HK¹-US⁰
//
// - NodeWithoutCF: HK²
//
// - 前两位字母是实际浏览网站识别的位置, -US⁰为使用CF CDN服务的网站识别的位置, 比如GPT, X等
func GetProxyCountry(httpClient *http.Client, db *maxminddb.Reader, getAnalyzedCtx context.Context, cfLoc string, cfIP string) (loc, ip, countryCodeTag, ispTag string, err error) {
	// 设置一个临时环境变量，以排除部分api因数据库更新不及时返回的 CN
	os.Setenv("SUBS-CHECK-PRO-CALL", "true")
	defer os.Unsetenv("SUBS-CHECK-PRO-CALL")

	cliMe, err := NewIPInfoClient(httpClient, db, ipAPIsMe, geoAPIsMe)
	if err != nil || cliMe == nil {
		slog.Debug("创建 MeAPI 客户端失败", "error", err)
		goto NextClient
	} else {
		defer func() {
			if cerr := cliMe.Close(); cerr != nil {
				slog.Warn("关闭 ipinfo 客户端失败", "err", cerr)
			}
		}()
	}

	for range config.GlobalConfig.SubUrlsReTry {
		loc, ip, countryCodeTag, ispTag, err = cliMe.GetAnalyzed(getAnalyzedCtx, cfLoc, cfIP)
		if err != nil {
			slog.Debug("MeAPI 获取节点位置失败", "error", err)
		}
		if loc != "" && countryCodeTag != "" {
			slog.Debug("MeAPI 获取节点位置成功", "ip", ip, "loc", loc, "code", countryCodeTag, "isp", ispTag)
			return loc, ip, countryCodeTag, ispTag, nil
		} else {
			slog.Debug("MeAPI 获取节点位置失败", "ip", ip, "loc", loc, "code", countryCodeTag)
		}
	}

NextClient:
	// 如失败，使用混合检测，不需要多次重试
	cli, err := NewIPInfoClient(httpClient, db, ipAPIs, geoAPIs)
	if err != nil || cli == nil {
		slog.Debug("创建 ipinfo 主客户端失败", "error", err)
	} else {
		defer func() {
			if cerr := cli.Close(); cerr != nil {
				slog.Warn("关闭 ipinfo 客户端失败", "err", cerr)
			}
		}()
		loc, ip, countryCodeTag, ispTag, err = cli.GetAnalyzed(getAnalyzedCtx, cfLoc, cfIP)
		if err != nil {
			slog.Debug("Analyzed 获取节点位置失败", "error", err)
			return "", "", "", "", err
		}
		if loc != "" && countryCodeTag != "" {
			slog.Debug("Analyzed 获取节点位置成功", "ip", ip, "loc", loc, "code", countryCodeTag, "isp", ispTag)
			return loc, ip, countryCodeTag, ispTag, nil
		} else {
			slog.Debug("Analyzed 获取节点位置空白", "ip", ip, "loc", loc, "code", countryCodeTag)
		}
	}
	return "", "", "", "", err
}

func GetEdgeOneProxy(httpClient *http.Client) (loc, ip string) {
	type GeoResponse struct {
		Eo struct {
			Geo struct {
				CountryCodeAlpha2 string `json:"countryCodeAlpha2"`
			} `json:"geo"`
			ClientIP string `json:"clientIp"`
		} `json:"eo"`
	}

	url := "https://functions-geolocation.edgeone.app/geo"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		slog.Debug("创建请求失败", "error", err)
		return
	}
	req.Header.Set("User-Agent", convert.RandUserAgent())
	resp, err := httpClient.Get(url)
	if err != nil {
		slog.Debug("edgeone获取节点位置失败", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("edgeone返回非200状态码", "StatusCode", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Debug("edgeone读取节点位置失败", "error", err)
		return
	}

	var eo GeoResponse
	err = json.Unmarshal(body, &eo)
	if err != nil {
		slog.Debug("解析edgeone JSON 失败", "error", err)
		return
	}

	return eo.Eo.Geo.CountryCodeAlpha2, eo.Eo.ClientIP
}

func GetCFProxy(httpClient *http.Client) (loc string, ip string) {
	url := "https://www.cloudflare.com/cdn-cgi/trace"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		slog.Debug("创建请求失败", "error", err)
		return
	}
	req.Header.Set("User-Agent", convert.RandUserAgent())
	resp, err := httpClient.Get(url)
	if err != nil {
		slog.Debug("cf获取节点位置失败", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("cf返回非200状态码", "StatusCode", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Debug("cf读取节点位置失败", "error", err)
		return
	}

	// Parse the response text to find loc=XX
	for line := range strings.SplitSeq(string(body), "\n") {
		if after, ok := strings.CutPrefix(line, "loc="); ok {
			loc = after
		}
		if after, ok := strings.CutPrefix(line, "ip="); ok {
			ip = after
		}
	}
	return
}

func GetIPLark(httpClient *http.Client) (loc string, ip string) {
	type GeoIPData struct {
		IP      string `json:"ip"`
		Country string `json:"country_code"`
	}

	url := string([]byte{104, 116, 116, 112, 115, 58, 47, 47, 102, 51, 98, 99, 97, 48, 101, 50, 56, 101, 54, 98, 46, 97, 97, 112, 113, 46, 110, 101, 116, 47, 105, 112, 97, 112, 105, 47, 105, 112, 99, 97, 116})
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		slog.Debug("创建请求失败", "error", err)
		return
	}
	req.Header.Set("User-Agent", "curl/8.7.1")
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Debug("iplark获取节点位置失败", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("iplark返回非200状态码", "StatusCode", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Debug("iplark读取节点位置失败", "error", err)
		return
	}

	var geo GeoIPData
	err = json.Unmarshal(body, &geo)
	if err != nil {
		slog.Debug("解析iplark JSON 失败", "error", err)
		return
	}

	return geo.Country, geo.IP
}

func GetMe(httpClient *http.Client) (loc string, ip string) {
	type GeoIPData struct {
		IP      string `json:"ip"`
		Country string `json:"country_code"`
	}

	url := "https://ip.122911.xyz/api/ipinfo"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		slog.Debug("创建请求失败", "error", err)
		return
	}
	req.Header.Set("User-Agent", "subs-check")
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Debug("me获取节点位置失败", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("me返回非200状态码", "StatusCode", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Debug("me读取节点位置失败", "error", err)
		return
	}

	var geo GeoIPData
	err = json.Unmarshal(body, &geo)
	if err != nil {
		slog.Debug("解析me JSON 失败", "error", err)
		return
	}

	return geo.Country, geo.IP
}
