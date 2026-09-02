# 开发指南（DEVELOPMENT.md）

> 本文件是给**后续开发者 / AI 助手**读的"提示词"与项目知识库。
> 开始任何功能开发前，请先完整阅读本文件与 `AGENTS.md`。
> 涉及本项目已有的功能（缩略图、115 登录），务必**先读对应源码**再动手，避免重复造轮子或破坏既有机制。

---

## 0. 项目一句话

基于上游 [OpenList](https://github.com/OpenListTeam/OpenList)（Go 后端 + Vite/SolidJS 前端）二次开发的**多网盘文件列表程序**，本仓库重点增强 **115 网盘**的扫码登录和媒体缩略图链路。

## 1. 仓库结构速览

```
main.go                 入口（go:embed public/dist）
frontend/               Vite + SolidJS + @hope-ui/solid（所有自定义 UI 源码）
frontend/src/pages/manage/thumb/Thumb.tsx     缩略图管理页
frontend/src/pages/manage/storages/*.tsx      存储管理（115 扫码登录、文件夹选择器）
server/router.go        gin 路由总表
server/handles/         HTTP handlers
  thumbadmin.go         缩略图管理 API
  mediathumb.go         缩略图生成/缓存/队列/受限 Range 读取核心
  driver115.go          115 扫码登录 API
internal/conf/          const.go 设置项 key；config.go 静态配置（proxy_address 等）
internal/model/         存储、任务等核心模型
internal/bootstrap/data/setting.go  设置默认值
drivers/115/            115 驱动（login、Link、上传、health.go 风控标记）
install.sh              VPS 一键安装
public/dist/            前端构建产物（仅占位 index.html 入库；go:embed 用）
```

## 2. 构建 / 部署 / 验证（每次改动必走）

> ⚠️ **顺序陷阱**：`go build` 必须在 `cp 前端产物到 public/dist` **之后**执行，否则二进制里是占位页（页面显示"前端尚未构建"）。

```bash
# 前端
cd frontend && pnpm install && pnpm build          # 产物在 frontend/dist/

# 拷贝 + 编译后端（顺序不能反）
cd ..
rm -rf public/dist && cp -r frontend/dist public/dist
go build -o openlist .

# 部署（生产 /root/OpenList_NEW，端口 5244，数据 /root/data）
# 找到旧进程 PID（用 readlink /proc/[0-9]*/exe 匹配，勿 pkill -f 自身）
kill <PID>; sleep 2
setsid nohup ./openlist server --data /root/data > /tmp/olt_prod.log 2>&1 &
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:5244/api/ping   # 期望 200
```

- 前端验证用 Playwright（`webapp-testing` skill）：登录 `admin / testadmin123`（生产密码见 config），检查交互与 console 错误。
- **提交前**：`git checkout -- public/dist/index.html`（还原占位，构建产物不入库）。
- 提交规范见 `AGENTS.md`（Conventional Commits；AI 助手非白名单时不加 Co-authored-by）。

---

## 3. 前端设计提示词 / 规范

### 3.1 技术栈与约定
- **框架**：SolidJS（响应式 `createSignal`/`createStore`），不是 React。
- **UI 库**：`@hope-ui/solid`。常用：`Button/Box/HStack/VStack/Tag/Text/Input/Select/Modal/Progress/Show/For`。
- **请求**：`import { r } from "~/utils"`（axios 实例，已带 `Authorization` 头），返回 `Resp`；成功用 `handleResp(resp, cb)`，带提示用 `handleRespWithNotifySuccess`。
- **提示**：`notify.success/info/warning/error`。
- **i18n**：文案一般在页面里直接写中文即可（本项目自定义页大量直接中文），驱动表单文案在 `frontend/src/lang/zh-CN/drivers.json`（en/zh-TW 同步）。
- **路由**：管理页在 `frontend/src/pages/manage/<name>/<Name>.tsx`，侧边栏入口在 `sidemenu_items.tsx`。

### 3.2 设计原则（给 AI 的"成熟前端提示词"）
1. **先读相邻代码**：新页面/组件先看 `Thumb.tsx` / `Logs.tsx` 的既有写法（组件、样式、请求模式），保持一致，不引入新依赖。
2. **状态最小化**：能用 `createSignal` 就别上 store；派生值用函数（`const x = () => ...`）。
3. **交互细节**：
   - 破坏性操作（删除/清空）必须 `window.confirm`。
   - 长列表用 `maxH` + `overflowY: auto`，不要全量渲染。
   - 异步按钮加 `loading`/`disabled` 防重复提交。
   - 空态要给提示文案（如"无缩略图""列表受限"），不要空白。
4. **响应式**：移动端用 `wrap`/`direction={{ "@initial": "column", "@md": "row" }}`。
5. **防呆**：任何列表接口可能因 115 风控失败 → 前端要有降级提示（参考 `Thumb.tsx` 的"列表受限"）。
6. **改完必须**：`pnpm build` 通过（vite 构建，不跑 tsc；现有 tsc 报错是历史遗留，不要被它阻塞，但**不要新增**未使用变量）。

### 3.3 前端常见坑
- Solid 的 `createEffect` 会跟踪其中读到的 signal：**不要在 effect 里读 A 又写 A**（会死循环，`FolderPicker115` 踩过）。用 `let wasOpened` 防抖。
- `Select`/`Radio` 的受控值用 `String(...)`，onChange 回调接 `(v) => save("manual", parseInt(v))`。
- 管理 API 的 `Authorization` 头：登录 JWT 或 `setting token` 均可；**不要加 "Bearer " 前缀**（服务端是常量比较）。

---

## 4. 后端开发提示词 / 规范

### 4.1 分层与模式
- **路由**：`server/router.go` 注册；管理接口挂在 `admin` 组（`/api/admin/...`，需 `Authorization` 头 = token 或 JWT，无 Bearer）。
- **Handler**：`server/handles/xxx.go`，入参 `ShouldBind(&req)`，出参 `common.SuccessResp/ErrorResp/ErrorStrResp`。
- **设置项**：新增设置 = `internal/conf/const.go` 加 key + `internal/bootstrap/data/setting.go` 加默认值；读写用 `setting.GetStr/GetInt`，保存用 `op.SaveSettingItems([]model.SettingItem{...})`。
- **运行时共享变量**（跨模块）：放 `internal/op/`，并用锁或原子类型保护。
- **驱动**：`drivers/115/`。新增能力时尽量复用 115driver 库方法；**115 API 细节**（UA/appVer/salt/风控）见下方 4.4。

### 4.2 新增功能的标准流程
1. 读本文件 + 读相关既有代码。
2. 后端：加设置项 → 加逻辑（尽量独立文件）→ `router.go` 挂路由 → `go build`。
3. 前端：对应页面加 UI → `pnpm build`。
4. 按第 2 节"构建/部署/验证"部署并实测（含 Playwright）。

### 4.3 核心子系统的"心智模型"
- **缩略图**：按数据面 / 任务面 / 控制面 / UI 四层维护。`mediathumb.go` 负责 ffmpeg、Range、缓存与内容身份等稳定数据面；`thumb_candidate.go` 是 3×3 候选数据面；`thumb_candidate_tasks.go` / `thumb_upload_tasks.go` 是任务面；`thumb_tree.go` 独立目录树 reconcile；`thumb_status.go` 负责低频统计与生成队列控制；`thumb_runtime.go` 提供高频轻量控制面；`thumbadmin.go` 只保留管理 CRUD、目录详情、排除/迁移和上传目标选择等编排。前端 `pages/manage/thumb/` 使用独立 API client、types、controller 与展示组件，`Thumb.tsx` 只做页面编排。
  - 队列：生成与上传都由 `github.com/OpenListTeam/tache` 管理，并通过原生 `task_items` 持久化；服务重启时 Running 自动恢复为 Pending，暂停/恢复直接 Pause/Start manager，清空会同步清除持久化数据，避免任务重启后复活。生成 manager 使用 `thumb_concurrency` 个 worker、保留 2048 任务安全上限和 `prewarmDone` 去重。
  - 上传限速：上传 manager 允许跨存储并行，但按 storage 独立限速/风控。115 默认单上传、10 次/5s；OneDrive 默认并发 2、40 次/5s；Local 默认并发 4。一个 115 挂载受限不会冻结其它存储上传。
  - 生成：`generateVideoThumb` → 本地片段抽帧 / 远程 Range 抽帧；同一路径通过 `singleflight` 去重。
  - `/vt`：浏览器缩略图请求只做内存/本地缓存命中；miss 立即返回短缓存占位图并加入持久化 tache 队列，远程恢复、Range 和 ffmpeg 都在后台执行，不阻塞文件列表。
  - 读取：严格校验 `206 Content-Range`，所有响应按请求长度限制；HTTP Client/Transport 按静态代理复用。
  - ffmpeg/ffprobe：有 `Link.URL` 时直接使用驱动 URL + Header；只有 `RangeReader` 的驱动通过进程内 `127.0.0.1` 随机 token Range Gateway 暴露给 libav。Gateway 单次 Range 最多 32MiB、生命周期绑定任务 context，不提供公网入口，也不使用外部 Host 拼接 `/d`。
  - 空白检测 `isBlankThumb`（纯色图当失败，不缓存）。
  - 元数据：数据库 `thumbnail_records` 保存已生成/已上传/排除状态、对象指纹、缓存键、远程文件名、失败分类/重试时间、最后发现/访问/生成时间和生成耗时；旧 `index.jsonl` / `cloud.jsonl` / `excluded.jsonl` 首次启动自动导入并改名为 `.migrated*` 备份，旧 `.fail` 读取时也会迁移到数据库。
  - 缓存键：新对象使用内容指纹（storage + object ID/path + size + mtime + hash + generation strategy version）而不是路径 MD5；升级前的旧缓存保留原键直到对象内容发生变化，避免升级时全量重建。同路径替换会自动失效旧 PNG、云端状态、失败状态、候选帧、moov 与时长缓存。
  - 生命周期清理：本地未被 DB 引用的 managed cache 超过 1h 自动回收；远端只识别本项目命名格式的 orphan，并且需连续观察 24h 后才删除，每小时最多校准 20 个目录，避免误删用户文件或突发 115 请求。
  - 失败重试：失败按 risk/auth/timeout/not_found/blank/range/media/policy/transient 分类并写入 `retry_after`；到期后自动允许重新入队，不再统一依赖 7 天 `.fail` TTL。
  - 状态轮询：`/api/admin/thumb/status` 不同步枚举远程 `_thumbnails` 目录；过期时先返回数据库/本地缓存已知统计，并在后台刷新远程真实计数，避免管理页 10s 轮询被 115 API 卡住。
  - 目录树：`/api/admin/thumb/tree` 首屏只读 DB/最近快照，立即返回；完整挂载递归扫描在后台 reconcile。每个挂载有独立 30s 预算（整轮最多 2min），慢 115 不会饿死后续挂载；只有扫描完整的挂载才参与删除已确认不存在的旧路径记录。若扫描为 partial，则以 DB 已知聚合值作为 `videos/cached/local/cloud` 下限并补齐缺失分支，目录数字不会因远端超时突然跳低。
  - 候选九宫格：`/admin/thumb/candidates` 只创建后台 job。最多 16 个活动任务、后台严格单路执行，并保留最多 32 个最近任务摘要/结果（30 分钟 TTL）；同一路径复用进行中任务。关闭预览弹窗或离开 Thumb 页面只会 detach UI，不会取消服务端任务；用户回来后可从“3×3 后台任务”继续看进度/结果，只有显式“取消任务”才 cancel。候选会等待普通生成任务让出资源，而不是要求用户先手动暂停生成队列。115 会先做一次最多 32MiB 的头部 Range 下载，并在本地片段上抽取 2~10s 的 9 个候选槽位；只有本地片段缺帧时才对缺失槽位执行远程 seek，减少 115 Range 次数。单次深偏移 `403/forbidden/pmt` 只视为当前帧拒绝，跳过后继续其它时间点；只有明确 `405/429/blocked/服务器开小差` 等硬风控信号才停止整组并标记存储 blocked，避免“一帧 403 导致整个 3×3 和整个挂载停 5 分钟”。
  - 控制面：`/api/admin/thumb/runtime` 每 2s 可安全轮询，只读取内存中的生成队列、上传队列、3×3 job、Tree reconcile 状态；不扫描磁盘、不访问网盘、不调用 ffmpeg。较重的缓存统计/metrics 仍由 `/status` 低频刷新，避免“为了显示进度反而制造 115 请求”。
  - 前端结构：`thumb/api.ts` 集中路由；`types.ts` 定义契约；`useCandidateController.ts` 管理 3×3 生命周期；`components/` 内分别维护 Overview、Generation/Upload Queue、Candidate Task Center、Candidate Modal、Tree、Directory Detail、Failure Log、Stale Path Migration。新增 UI 不应重新堆回 `Thumb.tsx`。
  - 可观测性：Thumb 状态页显示缓存命中率、生成平均/P95、Range URL/RangeReader/Loopback Gateway 次数和按失败类别统计。
  - 风控：`isStorageBlocked`（115 health 5 分钟窗口）。
- **静态出站代理**：默认直连；需要时只读取 `conf.Conf.ProxyAddress`。不要在运行中修改 115 的共享 Resty Client；多出口负载均衡应由外部基础设施完成。
- **115 登录**（`driver115.go` + `drivers/115/qrcode.go`）：二维码 → 轮询 → 写 Cookie → 文件夹选择器。

### 4.4 115 关键事实（改 115 前必读）
- 上传 API v4.0 `files/init`：**UA 必须是 `Mozilla/5.0 115Browser/<appVer>`**，否则 403"请升级到最新版本"（`getUploadUA`）。
- 下载/列表 API：普通 Chrome UA 更稳（`getUA`），WAF 对 115Browser 特征严。
- 风控特征错误：`405`/`blocked`/`服务器开小差` → `health.go` 标记 5 分钟风控。
- 认证失效：`ErrNotLogin`/`登录超时`/`user not login`/`no auth` 单独标记为 invalid，需要重新登录，不进入风控状态。
- 长视频 >90s 走 mosaic（9 帧拼图）；moov 在尾部自动远程抽帧。
- 配置的静态代理返回损坏字节时可能得到纯白图 → 已自动回退直连。

---

## 5. 验证清单（提交前自查）

- [ ] `go build -o openlist .` 通过
- [ ] `cd frontend && pnpm build` 通过（且产物已 `cp` 到 `public/dist`）
- [ ] 生产重启后 `ping=200`，前端页面不是"前端尚未构建"占位
- [ ] 新功能用 Playwright 实测过（登录 → 页面 → 交互）
- [ ] `git checkout -- public/dist/index.html` 后提交
- [ ] 提交信息遵循 Conventional Commits，无多余 trailer
