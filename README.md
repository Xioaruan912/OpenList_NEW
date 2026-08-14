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

新增后端 API：

| 端点 | 方法 | 说明 |
|---|---|---|
| `/api/115/qrcode` | GET | 获取登录二维码（base64 PNG）及 uid/time/sign |
| `/api/115/qrcode/status` | GET | 轮询扫码状态（0 等待 / 1 已扫描 / 2 已登录 / -1 过期 / -2 取消） |
| `/api/115/qrcode/login` | POST | 扫码确认后获取登录 Cookie（附账号信息） |
| `/api/115/root_folders` | POST | 使用 Cookie 列出网盘根目录文件夹 |
| `/api/115/check_cookie` | POST | 校验 Cookie 有效性，返回账号昵称 |
| `/api/115/check_storage` | POST | 校验已配置存储的 Cookie 有效性 |

> 注：原版默认的 `linux` / `mac` / `windows` 登录设备已被 115 官方下架，本版已将默认设备改为 `web`。

### 2. 视频缩略图

115 网盘中的视频文件（如 mp4）在列表中默认不显示内容预览，无法快速判断视频内容。

现在 **视频文件会自动生成缩略图**，在网格视图中直接显示视频画面：

- 首次访问时通过 ffmpeg 从视频流中抽取画面生成缩略图
- **浏览即预热**：打开目录时后台自动批量生成该目录所有视频的缩略图（默认开启，可设置 `thumb_prewarm` 关闭），无需等浏览器逐个加载
- 缩略图自动缓存到 `data/thumb_cache/`（不在 temp 目录，**服务重启不会丢失**），之后访问秒回
- 支持所有通过本程序挂载的视频存储（115 / 阿里云盘 / 本地等）

新增后端 API：

| 端点 | 方法 | 说明 |
|---|---|---|
| `/vt/*path` | GET | 获取视频文件缩略图（自动生成并缓存） |
| `/at/*path` | GET | 提取音频内嵌专辑封面 |
| `/it/*path` | GET | 图片文件缩略图 |
| `/ct/*path` | GET | 目录封面（自动查找 folder.jpg 等） |
| `/api/115/check_cookie` | POST | 校验 115 Cookie 是否有效（返回账号信息） |
| `/api/115/check_storage` | POST | 校验已配置的 115 存储 Cookie 是否有效 |

**缩略图增强特性**：

- 视频缩略图：优先下载开头 8MB 本地抽帧；遇到 moov 在文件尾部的视频（非快速启动编码）自动回退为 ffmpeg 远程 Range 抽帧，大视频也能快速出图
- 支持文件大小上限（默认视频 2GB / 音频 50MB / 图片 20MB，超限跳过），避免大文件卡住
- 抽帧失败会写入负缓存（7 天），不会反复重试
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
| 视频缩略图缓存 | `data/thumb_cache/`（重启保留） |
| 批量生成缩略图 | 管理后台接口 `POST /api/admin/thumb/generate`（body: path/recursive） |
| 缩略图状态 | 管理后台接口 `GET /api/admin/thumb/status` |
| 默认登录设备（115） | `web` |

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
首次访问每个视频需要下载视频片段并用 ffmpeg 抽帧，通常需要数秒到十几秒。生成后会缓存，之后秒开。如果视频很多，建议先浏览一次预热。超过 `thumb_video_max_size`（默认 2GB）的视频不会生成缩略图。
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
缩略图缓存在 `data/thumb_cache/`。系统会按 `thumb_cache_ttl`（默认 30 天）和 `thumb_cache_max_size`（默认 2GB）自动清理；也可手动删除该目录后重启服务。
</details>

<details>
<summary><b>缩略图加载还是很慢？</b></summary>
默认已开启「浏览即预热」，打开目录时后台自动生成缩略图。也可以手动批量生成：`POST /api/admin/thumb/generate`，body `{"path":"/目录路径","recursive":true}`。生成状态查询：`GET /api/admin/thumb/status`。
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
