# 自建测速地址（可选）

避免使用 Speedtest 或 Cloudflare 下载链接，因为部分节点会屏蔽测速网站。

1. 将 [`doc/cloudflare/worker.js`](https://github.com/sinspired/subs-check-pro/blob/main/doc/cloudflare/worker.js) 部署到 Cloudflare Workers。
2. 绑定自定义域名（避免被节点屏蔽）。
3. 在配置文件中设置 `speed-test-url` 为你的 Workers 地址：
4. 建议使用 `speed-test-url: random` 使用随机测速地址，避免对单个测速源造成负担。

```yaml
# 100MB
speed-test-url: https://custom-domain/speedtest?bytes=104857600
# 1GB
speed-test-url: https://custom-domain/speedtest?bytes=1073741824
```
