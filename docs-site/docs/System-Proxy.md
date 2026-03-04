# 系统代理与 GitHub 代理设置（可选）

新增：自动检测并设置系统代理；自动检测筛选 GitHub 代理并设置。

```yaml
# 新增设置项
# 优先级 1.system-proxy;2.github-proxy;3.ghproxy-group
# 即使未设置,也会检测常见端口(v2ray\clash)的系统代理自动设置

# 系统代理设置: 适用于拉取代理、消息推送、文件上传等等。
# 写法跟环境变量一样，修改需重启生效
# system-proxy: "http://username:password@192.168.1.1:7890"
# system-proxy: "socks5://username:password@192.168.1.1:7890"
system-proxy: ""
# Github 代理：获取订阅使用
# github-proxy: "https://ghfast.top/"
github-proxy: ""
# GitHub 代理列表：程序会自动筛选可用的 GitHub 代理
ghproxy-group:
# - https://ghp.yeye.f5.si/
# - https://git.llvho.com/
# - https://hub.885666.xyz/
# - https://p.jackyu.cn/
# - https://github.cnxiaobai.com/
```

如果拉取非 GitHub 订阅速度慢，可使用通用的 HTTP_PROXY / HTTPS_PROXY 环境变量加快速度；此变量不会影响节点测试速度。

```bash
# HTTP 代理示例
export HTTP_PROXY=http://username:password@192.168.1.1:7890
export HTTPS_PROXY=http://username:password@192.168.1.1:7890

# SOCKS5 代理示例
export HTTP_PROXY=socks5://username:password@192.168.1.1:7890
export HTTPS_PROXY=socks5://username:password@192.168.1.1:7890

# SOCKS5H 代理示例
export HTTP_PROXY=socks5h://username:password@192.168.1.1:7890
export HTTPS_PROXY=socks5h://username:password@192.168.1.1:7890
```

如果想加速github的链接，可使用网上公开的github proxy

可使用 GitHub 项目 [CF-Proxy](https://github.com/sinspired/CF-Proxy) 自建 GitHub 代理，可直接复制 [worker.js](https://github.com/sinspired/CF-Proxy/blob/main/worker.js) 到 [Cloudflare](https://dash.cloudflare.com/) 的 workers。

```yaml
# Github Proxy，获取订阅使用，结尾要带的 /
# github-proxy: "https://ghfast.top/"
github-proxy: "https://proxy.custom-domain/"
```
