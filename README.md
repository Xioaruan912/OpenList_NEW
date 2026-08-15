<div align="center">
  <h1>OpenList_NEW</h1>
  <p>基于 OpenList 二次开发的多网盘文件列表程序</p>
  <p>
    <img src="https://img.shields.io/badge/license-AGPL--3.0-blue.svg" alt="License" />
  </p>
</div>

---

## 简介

本项目基于 [OpenList](https://github.com/OpenListTeam/OpenList) 二次开发，在保留原版全部功能的基础上，增加了以下增强功能。

## 新增功能

### 1. 115 网盘扫码登录（免手动抓取 Cookie）

原先挂载 115 网盘需要用户手动运行 Python 脚本或从浏览器抓取 Cookie，过程繁琐。

现在在添加存储页面选择 **115 Cloud** 驱动后，会直接显示扫码登录面板：

- 可选择登录设备（网页端 / 安卓 / iOS / 电视端 / 支付宝小程序 / 微信小程序 / 安卓多开）
- 一键获取二维码，使用 115 App 扫码
- 扫码成功后 **自动填入 Cookie**，无需手动复制
- 自动弹出 **挂载文件夹选择框**，勾选需要的文件夹后自动填入根文件夹 ID
- **支持多根挂载**：可勾选多个文件夹（逗号分隔的 `root_folder_id` / `root_folder_ids`），挂载点下显示所选各文件夹
- **编辑时可重新选择**：存储编辑页点"选择挂载文件夹"，用已填 Cookie 直接打开选择器，支持逐级浏览、多选

新增后端 API：

| 端点 | 方法 | 说明 |
|---|---|---|
| `/api/115/qrcode` | GET | 获取登录二维码（base64 PNG）及 uid/time/sign |
| `/api/115/qrcode/status` | GET | 轮询扫码状态（0 等待 / 1 已扫描 / 2 已登录 / -1 过期 / -2 取消） |
| `/api/115/qrcode/login` | POST | 扫码确认后获取登录 Cookie（附账号信息） |
| `/api/115/root_folders` | POST | 使用 Cookie 列出网盘根目录文件夹 |
| `/api/115/list_folders` | POST | 逐级浏览指定文件夹的子文件夹（可视化选择器用） |
| `/api/115/check_cookie` | POST | 校验 Cookie 有效性，返回账号昵称 |
| `/api/115/check_storage` | POST | 校验已配置存储的 Cookie 有效性 |

> 注：原版默认的 `linux` / `mac` / `windows` 登录设备已被 115 官方下架，本版已将默认设备改为 `web`。

### 2. 视频缩略图

115 网盘中的视频文件（如 mp4）在列表中默认不显示内容预览，无法快速判断视频内容。

现在 **视频文件会自动生成缩略图**，在网格视图中直接显示视频画面：

- 首次访问时通过 ffmpeg 从视频流中抽取画面生成缩略图
- **长视频内容缩略图**：超过 90 秒的视频会从 10%~90% 均匀抽取 9 帧合成 **3×3 网格拼图**，展示视频各段内容而非只显示开头；抽帧失败自动降级为单帧
- **两种存储模式**（`thumb_store` 设置）：`local` 存服务器 `data/thumb_cache/`（重启不丢失）；`remote` 上传到视频同级目录的 `_thumbnails/` 文件夹（不占服务器磁盘，115 网盘内可读）
- 支持所有通过本程序挂载的视频存储（115 / 阿里云盘 / 本地等）

**缩略图管理后台**（侧边栏「缩略图」）：

- **目录树**：展示全部目录（含没有缩略图的），显示每目录「已缓存」数与「缺 N」标记，点击目录查看该目录已有缩略图的视频清单
- **生成**：点击目录的「生成」按钮、目录详情的「生成缺失 / 重建优化」，或「一键缩略图」弹窗选择起始目录（默认根目录）递归生成缺失缩略图
- **排除**：目录详情中勾选/取消视频，可排除不需要缩略图的视频（持久化到 `data/thumb_cache/excluded.jsonl`）
- **清空**：一键清空某目录所有缩略图（本地缓存 + 索引 + 远程 `_thumbnails`）
- **挂载迁移**：存储挂载路径变更后，一键把旧路径的缩略图索引与缓存迁移到新路径
- **防风控**：生成按 **5 个一批**节流提交（批间 20s），自动跳过处于 115 风控的存储，避免触发网盘限流

新增后端 API：

| 端点 | 方法 | 说明 |
|---|---|---|
| `/vt/*path` | GET | 获取视频文件缩略图（自动生成并缓存） |
| `/at/*path` | GET | 提取音频内嵌专辑封面 |
| `/it/*path` | GET | 图片文件缩略图 |
| `/ct/*path` | GET | 目录封面（自动查找 folder.jpg 等） |
| `/api/admin/thumb/status` | GET | 缩略图统计（缓存/队列/失败/占用/失效挂载） |
| `/api/admin/thumb/tree` | GET | 目录树（含无缩略图目录，videos/cached 统计） |
| `/api/admin/thumb/dir?path=` | GET | 指定目录已有缩略图视频清单与排除项 |
| `/api/admin/thumb/generate` | POST | 批量生成指定目录缩略图（body: path/recursive/force） |
| `/api/admin/thumb/retry_fails` | POST | 重试失败项（自动跳过风控存储） |
| `/api/admin/thumb/clear` | POST | 清空指定目录所有缩略图 |
| `/api/admin/thumb/exclude` | POST | 排除/恢复不需要缩略图的视频 |
| `/api/admin/thumb/migrate` | POST | 挂载路径变更后迁移缩略图索引与缓存 |
| `/api/115/check_cookie` | POST | 校验 115 Cookie 是否有效（返回账号信息） |
| `/api/115/check_storage` | POST | 校验已配置的 115 存储 Cookie 是否有效 |

**缩略图增强特性**：

- 视频缩略图：优先下载开头 3MB 本地抽帧；遇到 moov 在文件尾部的视频（非快速启动编码）自动回退为 ffmpeg 远程 Range 抽帧，大视频也能快速出图
- 长视频（>90s）自动生成 3×3 内容拼图缩略图；生成在后台按 5 个一批节流提交，避免触发网盘风控
- 支持文件大小上限（默认视频 2GB / 音频 50MB / 图片 20MB，超限跳过），避免大文件卡住
- 抽帧失败会写入失败标记（7 天），不会反复重试；后台失败重试带长间隔退避（180s，最多 3 次）
- 缓存自动清理：过期（默认 30 天）与总量（默认 2GB）超限时后台自动删除
- 缺失 ffmpeg 时视频/音频缩略图自动禁用并记录日志，不影响其他功能
- 网格视图下：视频显示视频画面、音频显示专辑封面、图片显示缩略图、目录显示封面图片

可通过管理后台「设置」调整以下项：

| 设置项 | 默认值 | 说明 |
|---|---|---|
| `thumb_video_max_size` | 2147483648 (2GB) | 视频缩略图最大文件大小（字节） |
| `thumb_audio_max_size` | 52428800 (50MB) | 音频封面最大文件大小（字节） |
| `thumb_image_max_size` | 20971520 (20MB) | 图片缩略图最大文件大小（字节） |
| `thumb_cache_ttl` | 30 | 缩略图缓存过期天数 |
| `thumb_cache_max_size` | 2147483648 (2GB) | 缩略图缓存总量上限（字节） |
| `thumb_cover_names` | folder.jpg,cover.jpg,... | 目录封面文件名（逗号分隔） |
| `thumb_dir_cover` | true | 网格视图是否显示目录封面 |
| `thumb_prewarm` | true | 浏览目录时后台预生成视频缩略图 |
| `thumb_concurrency` | 8 | 缩略图生成并发数 |
| `thumb_chunk_size` | 3145728 (3MB) | 抽帧下载的视频片段大小（字节） |

### 3. 一键安装脚本

根目录 `install.sh` 提供全新服务器一键部署：

```bash
# Debian / Ubuntu / CentOS 一键安装
curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/install.sh | bash
```

脚本自动完成：
1. 安装系统依赖（git、gcc、ffmpeg、Go）
2. 克隆本仓库并编译
3. 初始化数据目录
4. 注册 systemd 服务并开机自启
5. 启动并验证服务可用

可选环境变量：`INSTALL_DIR`（安装目录）、`OPENLIST_PORT`（端口）、`INSTALL_BRANCH`（分支）。

### 4. 115 缩略图防风控（多出口代理）

#### 背景

- 图片缩略图：115 列表接口对图片返回官方缩略图地址（`thumb.115.com`），本程序直接透出，**不触发任何下载**，无风控风险。
- 视频缩略图：115 列表接口对视频**不返回**缩略图（`u` 字段恒为空），只能由服务器下载视频片段本地 ffmpeg 抽帧生成。高频下载会触发 115 风控（`115 风控` / 接口拒绝）。

#### 缓解手段（三层）

1. **官方透出**：图片直接使用 115 官方缩略图，不占服务器流量。
2. **多出口代理**：视频片段下载与 115 API 请求可走用户自建的代理节点，**分散出口 IP**，降低单 IP 触发概率。
3. **后台节流 + 风控熔断**：缩略图生成按 5 个一批、批间 20s 提交；检测到存储处于 115 风控时自动跳过生成，风控解除后继续。

#### 115 存储的代理配置

在 115 存储的「代理」字段填入代理地址（留空则使用全局配置 `proxy_address`）：

| 格式 | 说明 |
|---|---|
| `http://host:port` | HTTP 代理 |
| `http://user:pass@host:port` | HTTP 代理（带认证） |
| `socks5://host:port` | SOCKS5 代理 |

该代理作用于 **115 API 请求**（登录、扫码、列目录）与 **缩略图相关下载**（视频片段抽帧、图片/音频/远程缩略图读取）。

> 说明：OpenList 端仅消费 `http://` / `socks5://` 代理（Go 标准库）。如果你希望用 `ss` 协议，请用下方脚本部署，脚本会自动额外开放一个 HTTP 端口供 OpenList 使用。

#### 一键部署代理脚本

仓库 `scripts/proxy/` 提供**一键部署脚本**，可把代理服务部署到你的任意 VPS（Debian/Ubuntu/CentOS/Alpine，x86_64/arm64）。

**在代理 VPS 上直接执行**：

```bash
# http 代理（OpenList 直接使用）
curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/scripts/proxy/proxy_deploy.sh \
  | bash -s -- --type http --port 1080 --password 你的密码

# ss 代理（自动补一个 http 端口供 OpenList 用）
curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/scripts/proxy/proxy_deploy.sh \
  | bash -s -- --type ss --port 8388 --password 你的密码 --http-port 1080
```

脚本自动完成：root 检查 → 检测架构下载 gost 静态二进制（或使用 `scripts/proxy/bin/` 下预置的 `gost-linux-<arch>` 离线包）→ 注册 systemd 服务并开机自启（无 systemd 的环境自动回退 nohup 后台运行）→ 输出部署摘要与 OpenList 填法。

可选参数：

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--type` | http | 代理协议：http / ss / socks5 |
| `--port` | 1080 | 代理端口 |
| `--password` | 自动生成 | 代理密码（自动生成时会在输出中打印） |
| `--http-port` | 端口+1000 | ss 模式下供 OpenList 使用的 http 端口 |
| `--traffic-port` | 9386 | （可选）trafficd 统计服务端口 |
| `--traffic-token` | 自动生成 | 统计鉴权 token（自动生成时会在输出中打印） |
| `--admin-ip` | 0.0.0.0/0 | 允许访问流量统计的网段（建议限定为 OpenList 服务器公网 IP） |
| `--dev` | 空 | 只统计指定网卡（如 eth0），默认统计全部网卡 |
| `--no-trafficd` | 关 | 不部署流量统计（默认仍部署，仅供命令行查看，管理后台不再依赖） |
| `--dry-run` | 关 | 只打印将执行的命令，不实际部署 |

#### 缩略图代理选择（管理后台）

在「缩略图管理」页可选择缩略图下载走哪个代理节点（**只作用于缩略图请求**，不影响 115 API / 列表请求）：

- **关闭（走全局代理）**：缩略图走存储代理或全局 `proxy_address`（默认）。
- **自动切换**：自动选择一个健康节点（最近最少使用），某节点连续失败 ≥3 次自动标记**风控**并切换其他节点，30 分钟后自动恢复；可在代理管理页手动「解除风控」。
- **手动指定**：固定走指定节点；该节点不可用（风控/停用）时自动回退到任一健康节点。

#### 流量统计

- **OpenList 侧**：节点累计流量（rx/tx）与实时速率由 OpenList 自身统计，**只统计经该代理节点的缩略图下载流量**，并非 VPS 整机流量。管理后台「代理管理」页可查看每个节点的状态、风控、实时速率与累计流量。
- **trafficd（可选）**：`scripts/proxy/` 内置的零依赖流量统计服务（`/proc/net/dev` 差值 + `/proc/net/tcp` 连接数，`GET /stats?token=xxx` 带网段白名单），统计的是**代理 VPS 整机网卡流量**，仅用于命令行 `./proxy_status.sh --host ... --traffic-token ...` 查看，管理后台不再依赖它。

管理后台 API：

| 端点 | 方法 | 说明 |
|---|---|---|
| `/api/admin/proxy/list` | GET | 节点列表 |
| `/api/admin/proxy/create` | POST | 新增节点 |
| `/api/admin/proxy/update` | POST | 更新节点 |
| `/api/admin/proxy/delete` | POST | 删除节点 |
| `/api/admin/proxy/traffic` | GET | 节点列表 + OpenList 侧统计（累计 rx/tx、实时速率、连接数、风控状态） |
| `/api/admin/proxy/recover` | POST | 手动解除节点风控 |
| `/api/admin/proxy/enable` | POST | 启用/停用节点 |
| `/api/admin/thumb/proxy` | GET/POST | 读取/保存缩略图代理选择（mode: off/auto/manual + node_id） |

## 快速部署

### 方式一：一键脚本（推荐）

```bash
curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/install.sh | bash
```

### 方式二：手动部署

```bash
# 1. 安装依赖
apt install -y git gcc ffmpeg curl     # Debian/Ubuntu
# 2. 获取 Go（或使用系统包管理器）
# 3. 克隆并编译
git clone https://github.com/Xioaruan912/OpenList_NEW.git
cd OpenList_NEW
go build -o openlist ./main.go
# 4. 启动
./openlist server
```

启动后浏览器访问 `http://<服务器IP>:5244`，首次启动的日志中会打印管理员账号和初始密码。

## 默认配置

| 项目 | 默认值 |
|---|---|
| Web 端口 | `5244` |
| 数据目录 | `<安装目录>/data/` |
| 数据库 | `data/data.db`（用户、存储配置、分享、任务） |
| 视频缩略图缓存 | `data/thumb_cache/`（重启保留） |
| 缩略图索引 | `data/thumb_cache/index.jsonl` |
| 排除列表 | `data/thumb_cache/excluded.jsonl` |
| 批量生成缩略图 | 管理后台「缩略图」页或 `POST /api/admin/thumb/generate` |
| 默认登录设备（115） | `web` |

## 数据备份与迁移

升级、重装或迁移服务器时，**只需备份整个数据目录**（默认 `<安装目录>/data/`，可通过 `-data` 启动参数或 `config.json` 调整），即包含全部配置与数据：

| 内容 | 路径 | 说明 |
|---|---|---|
| **数据库** | `data/data.db` | **最核心**：管理员账号、所有存储配置（含 115 Cookie / 各网盘 Token）、分享、任务、设置 |
| **缩略图缓存** | `data/thumb_cache/` | 已生成的视频/音频/图片缩略图（`*.png`），删除后会自动按需重新生成 |
| **缩略图索引** | `data/thumb_cache/index.jsonl` | 已生成缩略图的文件路径清单（备份后恢复无需重新扫描） |
| **排除列表** | `data/thumb_cache/excluded.jsonl` | 你手动排除（不需要缩略图）的视频列表 |
| **失败标记** | `data/thumb_cache/*.fail` | 生成失败的记录（7 天内不重试），可删 |
| **临时文件** | `data/temp/` | 上传/抽帧临时文件，可不备份 |
| **运行配置** | `data/config.json` | 端口等运行参数（若存在） |

### 备份方法

```bash
# 停止服务后复制（推荐，保证数据一致）
systemctl stop openlist
cp -a /opt/openlist/data /backup/openlist-data-$(date +%F)
systemctl start openlist

# 或在线备份数据库（SQLite 热备）
sqlite3 /opt/openlist/data/data.db ".backup '/backup/data.db.bak'"
```

> 提示：`thumb_cache/` 若体积较大（可设置 `thumb_cache_max_size` 上限），可只备份 `data.db` + `index.jsonl` + `excluded.jsonl`，缩略图丢失后会在浏览/生成时自动重建。

### 恢复 / 迁移

1. 全新安装（一键脚本或手动部署）后，**停止服务**
2. 将备份的 `data/` 覆盖到新安装的数据目录（保持 `data.db`、`thumb_cache/` 等）
3. 启动服务，登录原有账号即可；缩略图缓存与索引自动恢复，无需重新生成

### 一键安装脚本

根目录 `install.sh` 提供全新服务器一键部署：

```bash
# Debian / Ubuntu / CentOS 一键安装
curl -fsSL https://github.com/Xioaruan912/OpenList_NEW/raw/main/install.sh | bash
```

脚本自动完成：
1. 安装系统依赖（git、gcc、ffmpeg、Go）
2. 克隆本仓库并编译
3. 初始化数据目录
4. 注册 systemd 服务并开机自启
5. 启动并验证服务可用（`/ping`）

可选环境变量：`INSTALL_DIR`（安装目录）、`OPENLIST_PORT`（端口）、`INSTALL_BRANCH`（分支）。脚本已在纯净环境验证：仓库不包含任何数据文件，克隆后可直接 `go build ./main.go` 成功。

## 支持功能

- 多网盘聚合挂载（115、阿里云盘、百度网盘、GoogleDrive、OneDrive、WebDAV、FTP/SFTP、S3 等 90+ 驱动）
- 文件上传 / 下载 / 复制 / 移动 / 重命名 / 批量操作
- 在线预览（视频、图片、音频、文档）
- 分享链接、离线下载、任务管理
- WebDAV / S3 / FTP / SFTP 协议支持
- MCP（Model Context Protocol）支持

## 常见问题

<details>
<summary><b>115 扫码登录提示设备已下架？</b></summary>
请选择网页端、安卓、iOS、电视端、支付宝/微信小程序、安卓多开等设备，`linux` / `mac` / `windows` 已被 115 官方下架。
</details>

<details>
<summary><b>视频缩略图加载很慢？</b></summary>
首次访问每个视频需要下载视频片段并用 ffmpeg 抽帧，通常需要数秒到十几秒。生成后会缓存，之后秒开。可在管理后台「缩略图」页对需要展示的目录点击「生成」或使用「一键缩略图」批量预生成。超过 `thumb_video_max_size`（默认 2GB）的视频不会生成缩略图。
</details>

<details>
<summary><b>moov 在文件尾部的视频没有缩略图？</b></summary>
本版已内置处理：开头片段抽帧失败时，会自动回退为 ffmpeg 直接远程 Range 抽帧（只下载必要字节），moov 在尾部的视频也能正常出图。此回退依赖 ffmpeg 且存储需支持 Range 请求（115 / 本地 / 阿里云盘等均支持）。
</details>

<details>
<summary><b>音频没有显示封面？</b></summary>
只有内嵌了专辑封面（ID3 封面等）的音频文件才会显示封面；无封面的音频会在 7 天内不重复探测。可检查 `thumb_audio_max_size` 是否过小。
</details>

<details>
<summary><b>如何清理缩略图缓存？</b></summary>
管理后台「缩略图」页选中目录后点「清空」，会删除该目录所有缩略图（本地缓存、索引、远程 `_thumbnails`）。也可整体删除 `data/thumb_cache/` 后重启服务。系统还会按 `thumb_cache_ttl`（默认 30 天）和 `thumb_cache_max_size`（默认 2GB）自动清理。
</details>

<details>
<summary><b>缩略图加载还是很慢？</b></summary>
在管理后台「缩略图」页，展开目录树，对缺失缩略图的目录点击「生成」，或用「一键缩略图」选择起始目录批量生成。生成在后台按 5 个一批节流提交，避免触发网盘风控。已排除的视频不会生成缩略图。
</details>

<details>
<summary><b>某些视频不想生成缩略图？</b></summary>
管理后台「缩略图」页选中目录后，在右侧视频清单中取消勾选对应视频，点「排除未勾选」即可；之后批量生成会跳过它们。「恢复已排除」可撤销。
</details>

<details>
<summary><b>长视频缩略图只显示开头？</b></summary>
超过 90 秒的视频会自动生成 3×3 网格内容缩略图（从视频 10%~90% 均匀取样 9 帧）。对已有的长视频缩略图，可在管理后台「缩略图」页对目录点「重建优化」（force）重新生成。
</details>

<details>
<summary><b>修改了存储挂载路径后缩略图目录没更新？</b></summary>
管理后台「缩略图」页会检测到失效的旧挂载路径，在页面底部「挂载迁移」工具中填写旧/新前缀后点「迁移」，旧路径的缩略图缓存与索引会自动迁移到新路径。
</details>

<details>
<summary><b>如何修改端口？</b></summary>
修改 `<数据目录>/config.json` 中的 `scheme.http_port` 后重启服务即可。
</details>

## 致谢

- [OpenList](https://github.com/OpenListTeam/OpenList) - 上游项目
- 115 扫码登录方案参考 [ChenyangGao/qrcode_cookie_115](https://gist.github.com/ChenyangGao/d26a592a0aeb13465511c885d5c7ad61)

## 许可证

本项目遵循 [AGPL-3.0](LICENSE) 许可证。
