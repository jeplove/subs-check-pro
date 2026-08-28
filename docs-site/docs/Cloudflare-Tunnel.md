# ⚙️ Cloudflare Tunnel（隧道映射）外网访问

> WebUI 经过全新设计，添加了 logo 图标等资源，本地化了所有依赖，因此需要额外增加一个 `static` 资源路径。

## 🚀 简易操作步骤

1. 🔑 登录 Cloudflare (CF)，左侧菜单栏点击 `Zero Trust`
2. 🌐 在新页面，左侧菜单栏点击 `网络` → `Tunnels` → `创建隧道` → `选择 Cloudflared`
3. 🛠️ 按提示操作：
   - 为隧道命名
   - 安装并运行连接器
   - 路由隧道
4. ✅ 创建完成后，在 `Tunnels` 页面会出现你创建的隧道，点击隧道名称 → 编辑
5. ➕ 在隧道详情页点击 `已发布应用程序路由` → `添加已发布应用程序路由`
6. 🌍 配置主机名和服务：
   - 示例：`scp.你的域名.com/path`
     - `scp` → (可选) 子域
     - `你的域名` → 域名
     - `path` → (可选) 路径
   - 服务类型 → 选择 `http`
   - URL → 输入 `localhost:8199` 或 `localhost:8299`

## 📒 需添加的路由条目

> 本项目需要 `share-password` 才能访问 `./output`，可放心设置，谨慎分享。

> [!caution]
> 域名 `sub_store_for_subs_check` 不符合 RFC 域名规范（下划线 _ 不允许，顶级域名必须合法）。请替换为 `scp-store`。

### 🌐 使用子域映射端口

> `scp-store` 为订阅管理保留子域，请勿修改！

| 🏷️ 外网访问地址             | 💻 本地服务地址   | 💡 用途说明     |
| -------------------------- | ---------------- | -------------- |
| `scp.你的域名.com/*`       | `localhost:8199` | subs-check-pro |
| `scp-store.你的域名.com/*` | `localhost:8299` | scp-store      |

### 🔀 使用路径映射端口

| 外网访问地址                | 本地服务地址     | 用途说明          |
| --------------------------- | ---------------- | ----------------- |
| `scp.你的域名.com/admin`    | `localhost:8199` | 配置管理主页      |
| `scp.你的域名.com/static`   | `localhost:8199` | ico, js, css 文件 |
| `scp.你的域名.com/api`      | `localhost:8199` | 软件运行状态      |
| `scp.你的域名.com/analysis` | `localhost:8199` | 检测结果分析报告  |
| `scp.你的域名.com/files`    | `localhost:8199` | 内置文件服务      |
| `scp.你的域名.com/sub`      | `localhost:8199` | 🔒分享码分享       |
| `scp.你的域名.com/share`    | `localhost:8199` | 🔒分享码分享       |
| `scp.你的域名.com/more`     | `localhost:8199` | 🔒无密码分享       |

| 外网访问地址                              | 本地服务地址     | 用途说明        |
| ----------------------------------------- | ---------------- | --------------- |
| `scp-store.你的域名.com/{sub-store-path}` | `localhost:8299` | ❗sub-store 路径 |
| `scp-store.你的域名.com/share`            | `localhost:8299` | ❗sub-store 分享 |

## 🎉 使用方法

打开浏览器访问 `scp.你的域名.com/admin` → 输入 apiKey → 开始使用。

## 🌍 域名相关

Cloudflare Tunnel 外网访问需要域名，可自行注册或点击下方链接注册免费域名

[![域名注册](https://img.shields.io/badge/DigitalPlat-注册免费域名-2563eb?style=flat-square&logo=databricks&logoColor=ffffff)](https://dashboard.digitalplat.org/signup?ref=HZcosTVlmQ)


请勿用于非法用途！