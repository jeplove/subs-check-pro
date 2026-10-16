package check

import (
	"log/slog"

	_ "github.com/metacubex/mihomo/config"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/dns"
	"github.com/sinspired/subs-check-pro/v3/config"
)

// SetupMihomoDNS 初始化并注入底层的 mihomo DNS 解析器
func SetupMihomoDNS() {
	// 全局 IPv6 禁用控制
	resolver.DisableIPv6 = !config.GlobalConfig.EnableIPv6

	// 如果配置文件中没有指定自定义 DNS，则直接返回，使用 mihomo 默认的 Go 系统解析
	hasCustom := len(config.GlobalConfig.NameServer) > 0 || len(config.GlobalConfig.ProxyServerNameserver) > 0
	if !hasCustom {
		return
	}

	// 构造 mihomo 核心的 DNS 配置对象 (严格遵循底层的 dns.Config 结构)
	cfg := dns.Config{
		IPv6: config.GlobalConfig.EnableIPv6,
	}

	// 提前注入一个兜底的空 Resolver，防止在下方 ParseNameServer 解析过程中由于内部引用而触发 SystemResolver 的空指针 Panic
	fallbackResolver := dns.NewResolver(dns.Config{IPv6: cfg.IPv6})
	resolver.DefaultResolver = fallbackResolver
	resolver.SystemResolver = fallbackResolver
	resolver.ProxyServerHostResolver = fallbackResolver

	// 1. 解析 default-nameserver (传入字符串切片)
	defaults := []string{
		"114.114.114.114",
		"223.5.5.5",
		"8.8.8.8",
		"1.0.0.1"}

	if parsedDefaults, err := dns.ParseNameServer(defaults); err == nil {
		cfg.Default = parsedDefaults
	}

	// 立即注入临时的 Bootstrap Resolver，以防止接下来解析 DoH 时触发空指针 Panic
	// 此时 cfg 已经包含了 default-nameserver，具备了解析域名的能力
	bootstrapResolver := dns.NewResolver(cfg)
	resolver.DefaultResolver = bootstrapResolver
	resolver.SystemResolver = bootstrapResolver

	// 2. 解析 nameserver (主 DNS)
	if len(config.GlobalConfig.NameServer) > 0 {
		if parsedMain, err := dns.ParseNameServer(config.GlobalConfig.NameServer); err == nil {
			cfg.Main = parsedMain
		} else {
			slog.Warn("解析 nameserver 失败", "error", err)
		}
	}

	// 3. 解析 proxy-server-nameserver (节点专属域名解析 DNS)
	if len(config.GlobalConfig.ProxyServerNameserver) > 0 {
		if parsedProxy, err := dns.ParseNameServer(config.GlobalConfig.ProxyServerNameserver); err == nil {
			cfg.ProxyServer = parsedProxy
		} else {
			slog.Warn("解析 proxy-server-nameserver 失败", "error", err)
		}
	}

	// 实例化并将构造好的 Resolver 注入替换掉 Mihomo 的全局默认 Resolver
	finalResolver := dns.NewResolver(cfg)

	resolver.DefaultResolver = finalResolver
	// ProxyServerHostResolver 专门用于节点域名的解析
	resolver.ProxyServerHostResolver = finalResolver
	// 同时也更新 SystemResolver 避免后续任何兜底解析出现空指针
	resolver.SystemResolver = finalResolver

	slog.Info("使用自定义 DNS 配置",
		"nameserver", len(cfg.Main),
		"proxy-server-nameserver", len(cfg.ProxyServer))
}
