package proxies

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// ConvertsV2RayExtra convert V2Ray subscribe proxies data to mihomo proxies config
func ConvertsV2RayExtra(buf []byte) ([]map[string]any, error) {
	// TODO: 支持更多非标格式，支持标准mieru分享格式
	data := DecodeBase64(buf)

	arr := strings.Split(string(data), "\n")

	proxies := make([]map[string]any, 0, len(arr))
	names := make(map[string]int, 200)

	for _, line := range arr {
		line = strings.TrimRight(line, " \r")
		if line == "" {
			continue
		}

		scheme, body, found := strings.Cut(line, "://")
		if !found {
			continue
		}

		scheme = strings.ToLower(scheme)
		switch scheme {
		case "mieru":
			dcBuf, err := TryDecodeBase64Mihomo(body)
			if err != nil {
				continue
			}
			urlMieru, err := url.Parse("mieru://" + string(dcBuf))
			if err != nil {
				continue
			}

			slog.Debug("Mieru URL", "url", urlMieru.String(), "fragment", urlMieru.Fragment, "host", urlMieru.Hostname())

			query := urlMieru.Query()

			// name 优先取 fragment，否则取 profile，再 fallback 到 server:port
			name := urlMieru.Fragment
			if name == "" {
				if profile := query.Get("profile"); profile != "" {
					name = profile
				} else {
					name = fmt.Sprintf("%s:%s", urlMieru.Hostname(), query.Get("port"))
				}
			}
			name = uniqueName(names, name)

			mieru := make(map[string]any, 20)
			mieru["name"] = name
			mieru["type"] = "mieru"
			mieru["server"] = urlMieru.Hostname()

			// 端口和端口范围互斥
			if portRange := query.Get("port-range"); portRange != "" {
				mieru["port-range"] = portRange
			} else if port := query.Get("port"); port != "" {
				mieru["port"] = port
			}

			// transport 映射 protocol
			if transport := query.Get("protocol"); transport != "" {
				mieru["transport"] = strings.ToUpper(transport)
			} else {
				mieru["transport"] = "TCP"
			}

			// 用户名和密码
			mieru["username"] = urlMieru.User.Username()
			if pwd, ok := urlMieru.User.Password(); ok {
				mieru["password"] = pwd
			}

			// multiplexing 默认 MULTIPLEXING_LOW
			if mux := query.Get("multiplexing"); mux != "" {
				mieru["multiplexing"] = strings.ToUpper(mux)
			} else {
				mieru["multiplexing"] = "MULTIPLEXING_LOW"
			}

			// // 保留 profile 字段
			// if profile := query.Get("profile"); profile != "" {
			// 	mieru["profile"] = profile
			// }

			proxies = append(proxies, mieru)

		}
	}

	if len(proxies) == 0 {
		return nil, fmt.Errorf("convert v2ray subscribe error: format invalid")
	}

	return proxies, nil
}

func uniqueName(names map[string]int, name string) string {
	if index, ok := names[name]; ok {
		index++
		names[name] = index
		name = fmt.Sprintf("%s-%02d", name, index)
	} else {
		index = 0
		names[name] = index
	}
	return name
}
