<div align="center">
  <h1>OpenList_NEW</h1>
  <p>基于 <a href="https://github.com/OpenListTeam/OpenList">OpenList</a> 二次开发的多网盘文件列表程序，专为 <b>115 网盘</b>体验增强</p>
  <p>
    <img src="https://img.shields.io/badge/license-AGPL--3.0-blue.svg" alt="License" />
    <img src="https://img.shields.io/badge/frontend-vite%2Bsolid--js-blueviolet" alt="Frontend" />
    <img src="https://img.shields.io/badge/backend-Go-blue" alt="Backend" />
  </p>
</div>

---

## 功能特性

在保留上游 OpenList 全部能力（90+ 网盘驱动、在线预览、分享、离线下载、WebDAV/S3/FTP、MCP）的基础上，本分支新增：

| 特性 | 说明 |
|---|---|
| **115 扫码登录** | 添加存储页扫码登录 115，免手动抓 Cookie，支持多设备/多根挂载/可视化选文件夹 |
| **视频缩略图** | ffmpeg 自动为视频生成缩略图；长视频生成 3×3 内容拼图；音频取专辑封面；图片/目录缩略图 |
| **缩略图管理后台** | 目录树、批量生成/排除/清空、失败重试、**上传到网盘 `_thumbnails`**、**点击文件名查看缩略图**、**暂停/恢复/清空生成队列**，全部可视化 |
| **多出口防风控代理** | 缩略图下载可走自建代理节点分散出口 IP；节点一键部署、**真实连通性健康检查**、自动风控切换 |
| **全局代理策略** | 在「代理管理」页控制 115 访问侧出站代理（off/手动/auto）：**auto 检测到 115 风控时自动走健康节点，正常时直连** |
| **代理节点健康检查** | 通过节点实测 访问目标站/下载/上传，失败节点标记【不可用】并从选择中排除 |
| **一键安装** | 根目录 `install.sh` 全新 VPS 一条命令部署（自动编译前后端 + systemd 托管） |

---

## 快速开始（VPS 一键部署）

支持 Debian/Ubuntu（apt）与 CentOS/RHEL/Fedora（yum/dnf），需要 root：

```bash
curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/install.sh | bash
```

脚本自动完成：

1. 安装系统依赖（git、gcc、ffmpeg、curl）
2. 安装 Go 与 Node.js 22 + pnpm
3. 克隆仓库
4. **从源码构建前端**（`frontend/`）并拷贝到 `public/dist`
5. 编译后端二进制
6. 初始化数据目录、注册 systemd 服务并开机自启
7. 启动并验证 `/ping`

可选环境变量：

```bash
INSTALL_DIR=/opt/openlist   # 安装目录（默认 /opt/openlist）
INSTALL_BRANCH=main         # git 分支
OPENLIST_PORT=5244          # HTTP 端口
```

安装完成后访问 `http://<服务器IP>:5244`，管理员账号与初始密码打印在启动日志中：

```bash
journalctl -u openlist --no-pager | grep -i password
```

---

## 从源码构建（本地 / 二开）

仓库结构：

```
├── frontend/        # 前端源码（Vite + SolidJS + Hope UI），所有自定义页面都在这里
├── server/          # 后端 HTTP 层（gin 路由与 handlers）
├── internal/        # 后端核心（配置、数据库、存储驱动、缩略图逻辑）
├── drivers/         # 网盘驱动（115 等）
├── scripts/proxy/   # 代理节点一键部署脚本（proxy_deploy.sh / trafficd.py）
├── public/dist/     # 前端构建产物（go:embed 嵌入；源码见 frontend/，构建时生成）
├── main.go          # 入口
└── install.sh       # VPS 一键安装
```

**后端依赖 Go 1.25+，前端依赖 Node.js 20+ 与 pnpm。**

### 分步构建（二开推荐）

```bash
# 1. 构建前端
cd frontend
pnpm install
pnpm build            # 产物在 frontend/dist/

# 2. 拷贝前端产物到后端嵌入目录
cd ..
rm -rf public/dist && mkdir -p public/dist
cp -r frontend/dist/. public/dist/

# 3. 编译后端（go:embed all:dist 会将前端一起打进二进制）
go build -o openlist ./main.go

# 4. 运行
./openlist server
```

> `public/dist` 默认只保留一个占位 `index.html`（保证空仓库也能 `go build`）。**构建产物不入库**，前端源码即 `frontend/`，二开直接改 `frontend/src/` 即可。

> 根目录 `build.sh` 是上游的多平台发布脚本（交叉编译需要 xgo/Docker），已改为使用本仓库 `frontend/` 源码构建前端（不再下载上游 UI）；日常开发请用上面的分步构建。

### 前端本地开发（热更新）

```bash
cd frontend
pnpm install
pnpm dev        # Vite dev server，API 走 .env.development 指向的后端
```

本地开发时后端单独跑 `./openlist server`，前端 `pnpm dev` 提供热重载，改完 `frontend/src/pages/manage/thumb/Thumb.tsx` 之类的页面即时生效。

### 主要自定义页面 / 路由（前端）

| 前端路由 | 文件 | 说明 |
|---|---|---|
| `@manage/thumb` | `frontend/src/pages/manage/thumb/Thumb.tsx` | 缩略图管理（生成/目录树/代理选择） |
| `@manage/proxy` | `frontend/src/pages/manage/proxy/Proxy.tsx` | 代理节点管理（增删改、安装命令、监控） |
| `@manage/storages` | `frontend/src/pages/manage/storages/Storages.tsx` | 存储管理（含 115 扫码登录、缩略图状态） |
| `@manage` | `frontend/src/pages/manage/sidemenu_items.tsx` | 侧边栏菜单 |

---

## 核心功能详解

### 1. 115 网盘扫码登录

添加存储页选择 **115 Cloud** 驱动后直接显示扫码面板：

- 可选择登录设备（网页 / 安卓 / iOS / 电视 / 支付宝/微信小程序 / 安卓多开；`linux`/`mac`/`windows` 已被 115 下架）
- 一键获取二维码 → 115 App 扫码 → 自动填入 Cookie
- 自动弹出挂载文件夹选择框，支持**多根挂载**与编辑时重新选择

### 2. 视频缩略图

- 视频自动生成缩略图（网格视图直接显示画面）；**长视频（>90s）从 10%~90% 抽 9 帧合成 3×3 内容拼图**
- 存储模式（`thumb_store`）：`local` 存服务器 `data/thumb_cache/`；`remote` 上传到网盘视频同级 `_thumbnails/` 文件夹（不占服务器磁盘）
- moov 在尾部的非快速启动视频自动回退 ffmpeg 远程 Range 抽帧
- 缩略图管理后台提供目录树、批量生成/重建/排除/清空、失败重试、挂载迁移
- 常见问题与调优见文末 FAQ

### 3. 多出口防风控代理

115 网盘对单个出口 IP 的请求频率敏感，本功能让缩略图下载与 115 访问分散到多个自建代理节点，降低风控触发概率。

**部署链路：节点配置 → 复制安装命令 → VPS 一键部署 → OpenList 监控 + 使用**

1. 「代理管理」页**新增节点**（名称/协议 http 或 ss/地址/端口/密码；`ss` 节点按 SOCKS5 连接）
2. 点节点上的「**安装命令**」按钮，复制生成的命令
3. 在节点 VPS 上以 root 执行该命令，自动部署 `gost` 代理 + `trafficd` 流量探针（systemd 托管，重启自启）：
   ```bash
   curl -fsSL http://<OpenList地址>/api/proxy/install.sh | bash -s -- --type http --port 1080 --password 你的密码
   ```
   > 安装脚本由 OpenList 服务直接下发（`/api/proxy/install.sh`，免登录），不依赖 GitHub 可达性。
4. 约 10 秒后节点显示「**探针在线**」，同时 OpenList 会**通过节点实测 访问目标站/下载/上传**做真实连通性检查：失败节点标红【不可用】并从自动选择中排除，连通恢复正常自动解除
5. 「缩略图」页选择代理模式（控制**缩略图下载/抽帧**）：
   - **关闭**：走存储代理 / 全局 `proxy_address`（默认）
   - **自动切换**：自动选择最近最少使用的健康节点；节点连续失败 ≥3 次自动标记**风控**并切换，30 分钟后自动恢复
   - **手动指定**：固定走指定节点，不可用时自动回退健康节点
6. 「代理管理」页顶部的**全局代理策略**（控制 **115 访问侧**——列表/搜索/上传等驱动请求）：
   - **关闭**：直连（默认）
   - **自动（仅风控时走代理）**：检测到任一 115 挂载风控时自动切到健康节点，正常时直连（反应式，30s 轮询）
   - **手动指定**：始终走指定节点
   - `/d` 下载与在线播放保持 302 直连 CDN（快，不经过代理节点）

> 流量统计口径：管理后台显示的累计/速率**只统计经该节点的缩略图下载流量**（OpenList 侧计数），非 VPS 整机流量。

---

## 管理 API 一览

### 115 登录

| 端点 | 方法 | 说明 |
|---|---|---|
| `/api/115/qrcode` | GET | 获取登录二维码 |
| `/api/115/qrcode/status` | GET | 轮询扫码状态 |
| `/api/115/qrcode/login` | POST | 获取登录 Cookie |
| `/api/115/root_folders` | POST | 列出根目录文件夹 |
| `/api/115/list_folders` | POST | 逐级浏览文件夹 |
| `/api/115/check_cookie` | POST | 校验 Cookie |
| `/api/115/check_storage` | POST | 校验存储 Cookie |

### 缩略图

| 端点 | 方法 | 说明 |
|---|---|---|
| `/vt/*path` `/at/*path` `/it/*path` `/ct/*path` | GET | 视频 / 音频 / 图片 / 目录封面 |
| `/api/admin/thumb/status` | GET | 缩略图统计 |
| `/api/admin/thumb/tree` | GET | 目录树 |
| `/api/admin/thumb/dir` | GET | 指定目录所有媒体文件清单（含 has_thumb 标记） |
| `/api/admin/thumb/view` | GET | 返回指定视频缩略图 PNG（读缓存/生成） |
| `/api/admin/thumb/generate` | POST | 批量生成（只统计真正缺失的，递归） |
| `/api/admin/thumb/upload` | POST | 将本地缩略图上传到网盘 `_thumbnails/` |
| `/api/admin/thumb/delete_folder` | POST | 删除目录的 `_thumbnails` 文件夹并清本地缓存 |
| `/api/admin/thumb/queue/pause` `/resume` `/clear` | POST | 暂停 / 恢复 / 清空生成队列 |
| `/api/admin/thumb/retry_fails` | POST | 重试失败项 |
| `/api/admin/thumb/clear` | POST | 清空目录缩略图 |
| `/api/admin/thumb/exclude` | POST | 排除/恢复视频 |
| `/api/admin/thumb/migrate` | POST | 挂载路径迁移 |

### 代理

| 端点 | 方法 | 说明 |
|---|---|---|
| `/api/proxy/install.sh` | GET | 下发部署脚本（免登录） |
| `/api/admin/proxy/list` | GET | 节点列表 |
| `/api/admin/proxy/create` `/update` `/delete` | POST | 节点增删改 |
| `/api/admin/proxy/install` | GET | 生成一键安装命令 |
| `/api/admin/proxy/traffic` | GET | 节点 + 流量统计 + 探针状态 + 连通性健康 |
| `/api/admin/proxy/policy` | GET/POST | 全局代理策略（off/auto/manual + 节点） |
| `/api/admin/proxy/recover` | POST | 解除风控 |
| `/api/admin/proxy/enable` | POST | 启用/停用 |
| `/api/admin/thumb/proxy` | GET/POST | 缩略图代理模式（off/auto/manual） |

---

## 数据备份与迁移

升级、重装或换服务器时，**只需备份整个数据目录**（默认 `<安装目录>/data/`）：

| 内容 | 路径 | 说明 |
|---|---|---|
| **数据库** | `data/data.db` | 管理员账号、存储配置（含 115 Cookie）、分享、任务、设置 |
| **缩略图缓存** | `data/thumb_cache/` | 生成的缩略图（可设置容量上限） |
| **缩略图索引** | `data/thumb_cache/index.jsonl` | 已生成缩略图清单 |
| **排除列表** | `data/thumb_cache/excluded.jsonl` | 手动排除的视频 |
| **运行配置** | `data/config.json` | 端口等 |

```bash
# 备份
systemctl stop openlist
cp -a /opt/openlist/data /backup/openlist-data-$(date +%F)
systemctl start openlist

# 迁移：新机器安装后，停止服务，覆盖 data/，再启动即可
```

---

## 常见问题

<details>
<summary><b>如何修改端口？</b></summary>
修改 `<数据目录>/config.json` 中的 `scheme.http_port` 后重启服务。
</details>

<details>
<summary><b>115 扫码登录提示设备已下架？</b></summary>
选择网页端、安卓、iOS、电视端、支付宝/微信小程序、安卓多开等设备；`linux`/`mac`/`windows` 已被 115 官方下架。
</details>

<details>
<summary><b>视频缩略图加载很慢？</b></summary>
首次访问需下载视频片段 + ffmpeg 抽帧（数秒~十几秒），之后秒开。可在「缩略图」页对目标目录点「生成」批量预生成。超过 `thumb_video_max_size`（默认 2GB）的视频不生成。
</details>

<details>
<summary><b>长视频只显示开头画面？</b></summary>
超过 90 秒的视频会自动生成 3×3 内容拼图，无需手动处理。
</details>

<details>
<summary><b>想释放服务器磁盘（缩略图存网盘）？</b></summary>
存储配置里把 `thumb_store` 设为 `remote`，生成后缩略图会上传到视频同级的 `_thumbnails/` 文件夹；也可在「缩略图」页对目录点「上传」把已生成的本地缩略图推送到网盘。
</details>

<details>
<summary><b>如何清理缩略图缓存？</b></summary>
「缩略图」页选中目录点「清空」删除该目录缩略图；或点目录行的「删除」直接删掉网盘 `_thumbnails` 文件夹。系统会按 `thumb_cache_ttl`（默认 30 天）与 `thumb_cache_max_size`（默认 2GB）自动清理。
</details>

<details>
<summary><b>代理节点显示风控中 / 不可用？</b></summary>
连续失败 ≥3 次会自动标记风控并切换健康节点，30 分钟后自动恢复；「不可用」表示节点真实连通性检查失败（如 gost 认证失败、超时），请检查节点密码与网络，连通恢复正常后自动解除。
</details>

<details>
<summary><b>115 提示风控（405/WAF 拦截）？</b></summary>
这是 115 对当前出口 IP 的风控。可在「代理管理」页开启「全局代理策略」为 auto，检测到风控后自动切换到代理节点访问，正常时恢复直连。
</details>

<details>
<summary><b>想二开 / 提交自定义页面？</b></summary>
前端全部源码在 `frontend/src/`，改完 `pnpm build` 后按「从源码构建」步骤重编后端即可；详见上方「前端本地开发」。
</details>

---

## 致谢

- [OpenList](https://github.com/OpenListTeam/OpenList) — 上游项目
- 115 扫码登录方案参考 [ChenyangGao/qrcode_cookie_115](https://gist.github.com/ChenyangGao/d26a592a0aeb13465511c885d5c7ad61)

## 许可证

[AGPL-3.0](LICENSE)