import {
  Box,
  Button,
  Grid,
  HStack,
  Input,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Progress,
  ProgressIndicator,
  ProgressLabel,
  Tag,
  Text,
  VStack,
  useColorModeValue,
  Switch as HopeSwitch,
} from "@hope-ui/solid"
import { createSignal, createEffect, For, Show, onCleanup } from "solid-js"
import { useManageTitle } from "~/hooks"
import { handleResp, handleRespWithoutNotify, notify, r } from "~/utils"

type ThumbStatus = {
  cached_files: number
  local_files: number
  cloud_files: number
  fail_markers: number
  cache_size: number
  cache_dir: string
  prewarm_queued: number
  queue_paused: boolean
  prewarm_enabled: boolean
  auto_upload: boolean
  active_workers: number
  active_tasks?: { path: string; since: number }[]
  fail_items?: { path: string; dir: string; msg: string; at: string }[]
  blocked: boolean
  stale_by_dir?: { dir: string; count: number }[]
  mounts?: string[]
  metrics?: {
    cache_hits: number
    cache_misses: number
    cache_hit_rate: number
    placeholders: number
    generated: number
    generation_failed: number
    avg_generate_ms: number
    p95_generate_ms: number
    range_http: number
    range_reader: number
    range_gateway: number
    failures?: Record<string, number>
  }
}

type ThumbCandidate = {
  index: number
  at: string
  png: string
}

type ThumbCandidatesData = {
  job_id?: string
  state?: "queued" | "running" | "succeeded" | "failed" | "canceled"
  done?: number
  total?: number
  progress?: number
  error?: string
  candidates?: ThumbCandidate[]
  sheet?: string
  recommended_index?: number
  cached?: boolean
  risk_blocked?: boolean
  truncated?: boolean
}

type TreeNode = {
  path: string
  name: string
  cached: number
  local?: number
  cloud?: number
  videos?: number
  children?: TreeNode[]
}

const Thumb = () => {
  useManageTitle("缩略图管理")
  const [st, setSt] = createSignal<ThumbStatus>()
  const [tree, setTree] = createSignal<TreeNode[]>([])
  const [queued, setQueued] = createSignal(0)
  const [genActive, setGenActive] = createSignal(0)
  const [genBlocked, setGenBlocked] = createSignal(false)
  const [totalQueued, setTotalQueued] = createSignal(0)
  const [baseCached, setBaseCached] = createSignal<number | null>(null)
  const [busy, setBusy] = createSignal("")
  const [sel, setSel] = createSignal("")
  const [selFiles, setSelFiles] = createSignal<string[]>([])
  const [selCount, setSelCount] = createSignal(0)
  const [selExcluded, setSelExcluded] = createSignal<string[]>([])
  const [hasThumb, setHasThumb] = createSignal<Record<string, boolean>>({})
  const [dirListed, setDirListed] = createSignal(true)
  const [checked, setChecked] = createSignal<Record<string, boolean>>({})
  const [expanded, setExp] = createSignal<Set<string>>(new Set())
  const [treeLoading, setTreeLoading] = createSignal(false)
  const [stale, setStale] = createSignal<{ dir: string; count: number }[]>([])
  const [mounts, setMounts] = createSignal<string[]>([])
  const [oldP, setOldP] = createSignal("")
  const [newP, setNewP] = createSignal("")
  const [viewPath, setViewPath] = createSignal("")
  const [viewUrl, setViewUrl] = createSignal("")
  const [viewLoading, setViewLoading] = createSignal(false)
  const [cands, setCands] = createSignal<ThumbCandidate[]>([])
  const [candLoading, setCandLoading] = createSignal(false)
  const [candSheet, setCandSheet] = createSignal("")
  const [recommendedIndex, setRecommendedIndex] = createSignal(0)
  const [candCached, setCandCached] = createSignal(false)
  const [candRiskBlocked, setCandRiskBlocked] = createSignal(false)
  const [candTruncated, setCandTruncated] = createSignal(false)
  const [candJobId, setCandJobId] = createSignal("")
  const [candProgress, setCandProgress] = createSignal(0)
  const [applying, setApplying] = createSignal(false)
  const [knownFails, setKnownFails] = createSignal<Set<string>>(new Set())
  const [failedMap, setFailedMap] = createSignal<Record<string, string>>({})
  let candidateRequestId = 0
  let candidateRequestActive = false
  let treeRefreshTimer: ReturnType<typeof setTimeout> | undefined
  const [treeScanStatus, setTreeScanStatus] = createSignal("")
  let firstStatusLoaded = false
  const [uploadLive, setUploadLive] = createSignal(false) // 本次会话是否有上传运行（控制轮询）
  const upDefault = {
    active: false,
    paused: false,
    queued: 0,
    remaining: 0,
    done: 0,
    failed: 0,
    exists: 0,
    fails: 0,
    total: 0,
    fail_items: [] as { path: string; msg: string }[],
  }
  const [upStatus, setUpStatus] = createSignal<typeof upDefault>(upDefault)
  const [logOpen, setLogOpen] = createSignal(false)
  const [logItems, setLogItems] = createSignal<{ path: string; msg: string; at: string }[]>([])

  // 失败日志：path 为空查看全部，否则查看单个文件
  const openFailLog = (path?: string) => {
    const items = (st()?.fail_items || []).map((i) => ({
      path: i.path,
      msg: i.msg || "生成失败",
      at: i.at || "",
    }))
    if (path) {
      const hit = items.find((i) => i.path === path)
      if (hit) {
        setLogItems([hit])
      } else {
        setLogItems(failedMap()[path] ? [{ path, msg: failedMap()[path]!, at: "" }] : [])
      }
    } else {
      setLogItems(items)
    }
    setLogOpen(true)
  }

  // 上传失败日志
  const openUploadLog = () => {
    setLogItems(
      (upStatus()?.fail_items || []).map((i) => ({
        path: i.path,
        msg: i.msg || "上传失败",
        at: "",
      })),
    )
    setLogOpen(true)
  }

  const load = async () => {
    const resp = await r.get("/admin/thumb/status")
    handleResp(resp, (d) => {
      const data = d as ThumbStatus
      setSt(data)
      setQueued(data.prewarm_queued || 0)
      if (baseCached() === null) setBaseCached(data.cached_files || 0)
      setStale(data.stale_by_dir || [])
      setMounts(data.mounts || [])
      setOldP((data.stale_by_dir || [])[0]?.dir.split("/").slice(0, 2).join("/") || "")
      setNewP((data.mounts || [])[0] || "")
      setGenActive(data.active_workers || 0)
      setGenBlocked(!!data.blocked)
      // 队列排空且无进行中任务：重置本次进度（避免跨批次累积导致进度条不准确）
      if ((data.prewarm_queued || 0) === 0 && (data.active_workers || 0) === 0) {
        setTotalQueued(0)
      }
      // 检测新增失败并告警（首次加载不弹，之后新增失败才弹）
      const items = data.fail_items || []
      if (firstStatusLoaded) {
        const newOnes = items.filter((i) => !knownFails().has(i.path))
        if (newOnes.length > 0) {
          const samples = newOnes
            .slice(0, 5)
            .map((i) => `${i.path.split("/").pop()}（${i.msg || "生成失败"}）`)
            .join("、")
          notify.error(
            `${newOnes.length} 个缩略图生成失败：${samples}` +
              (newOnes.length > 5 ? ` 等 ${newOnes.length} 个` : ""),
          )
        }
      }
      firstStatusLoaded = true
      setKnownFails(new Set(items.map((i) => i.path)))
    })
  }

  // 自动生成 / 自动上传开关：保存设置并刷新状态
  const setAuto = async (generate?: boolean, upload?: boolean) => {
    const resp = await r.post("/admin/thumb/auto", {
      ...(generate !== undefined ? { generate } : {}),
      ...(upload !== undefined ? { upload } : {}),
    })
    handleResp(resp, () => void load())
  }

  const loadTree = async () => {
    if (treeRefreshTimer) {
      clearTimeout(treeRefreshTimer)
      treeRefreshTimer = undefined
    }
    if (!tree().length) setTreeLoading(true)
    try {
      const resp = await r.get("/admin/thumb/tree")
      handleResp(resp, (d) => {
        const data = d as { children?: TreeNode[]; scan_status?: string }
        setTree(data.children || [])
        setTreeScanStatus(data.scan_status || "")
        if (data.scan_status === "refreshing") {
          treeRefreshTimer = setTimeout(() => void loadTree(), 3000)
        }
      })
    } finally {
      setTreeLoading(false)
    }
  }

  const loadDir = async (pp: string) => {
    if (!pp) return
    const resp = await r.get("/admin/thumb/dir", { params: { path: pp } })
    handleResp(resp, (d) => {
      const data = d as {
        files?: string[]
        count?: number
        excluded?: string[]
        has_thumb?: Record<string, boolean>
        failed?: Record<string, string>
        listed?: boolean
      }
      setSelFiles(data.files || [])
      setSelCount(data.count || 0)
      setSelExcluded(data.excluded || [])
      setHasThumb(data.has_thumb || {})
      setFailedMap(data.failed || {})
      setDirListed(!!data.listed)
      const z: Record<string, boolean> = {}
      for (const f of data.files || []) {
        z[f] = !(data.excluded || []).includes(f)
      }
      setChecked(z)
    })
  }

  const queueGen = async (pp: string, force: boolean) => {
    setBusy(pp)
    try {
      const resp = await r.post("/admin/thumb/generate", {
        path: pp,
        recursive: true,
        force: !!force,
      })
      handleResp(resp, (d) => {
        const data = d as { queued?: number; blocked?: boolean; truncated?: boolean }
        notify.success(
          `已加入队列：${data.queued || 0} 个` +
            (data.truncated ? "（已达单次上限，可再次点击分批生成）" : ""),
        )
        setTotalQueued((t) => t + (data.queued || 0))
        load()
      })
    } finally {
      setBusy("")
    }
  }

  // 只对当前目录（非递归）生成缺失缩略图
  const genSelDir = async () => {
    if (!sel()) {
      notify.warning("请先选择目录")
      return
    }
    setBusy("gen-" + sel())
    try {
      const resp = await r.post("/admin/thumb/generate", {
        path: sel(),
        recursive: false,
      })
      handleResp(resp, (d) => {
        const data = d as { queued?: number }
        notify.success(`已加入队列：${data.queued || 0} 个（仅当前目录）`)
        setTotalQueued((t) => t + (data.queued || 0))
        load()
      })
    } finally {
      setBusy("")
    }
  }

  // 删除勾选的缩略图（本地缓存 + 索引，remote 模式同步删网盘文件）
  const deleteChecked = async () => {
    if (!sel()) {
      notify.warning("请先选择目录")
      return
    }
    const paths = selFiles().filter((p) => checked()[p])
    if (!paths.length) {
      notify.warning("没有勾选的缩略图可删除")
      return
    }
    if (!window.confirm(`确认删除勾选的 ${paths.length} 个缩略图？（${sel()}）`)) return
    setBusy("delpaths-" + sel())
    try {
      const resp = await r.post("/admin/thumb/delete_paths", { paths })
      handleResp(resp, (d) => {
        const data = d as { removed?: number; remote_skipped?: boolean }
        notify.success(
          `已删除 ${data.removed || 0} 个缩略图` +
            (data.remote_skipped ? "（115 风控中，远程待恢复后清理）" : ""),
        )
        loadTree()
        loadDir(sel())
        load()
      })
    } finally {
      setBusy("")
    }
  }

  const uploadDir = async (pp: string) => {
    setBusy("up-" + pp)
    try {
      const resp = await r.post("/admin/thumb/upload", { path: pp })
      handleResp(resp, (d) => {
        const data = d as { queued?: number }
        if (!data.queued) {
          notify.info("该目录没有可上传的本地缩略图")
        } else {
          notify.success(`已加入上传队列 ${data.queued} 个（每批 50，间隔 5 秒）`)
          const n = data.queued || 0
          setUpStatus((s) => ({
            ...s,
            active: true,
            total: (s?.total || 0) + n,
            remaining: (s?.remaining || 0) + n,
          }))
          setUploadLive(true)
          void pollUploadStatus()
        }
      })
    } finally {
      setBusy("")
    }
  }

  const uploadAll = async () => {
    setBusy("upload-all")
    try {
      const resp = await r.post("/admin/thumb/upload_all", {})
      handleResp(resp, (d) => {
        const data = d as { queued?: number }
        if (!data.queued) {
          notify.info("没有可上传的本地缩略图")
        } else {
          notify.success(`已加入上传队列 ${data.queued} 个（每批 50，间隔 5 秒）`)
          const n = data.queued || 0
          setUpStatus((s) => ({
            ...s,
            active: true,
            total: (s?.total || 0) + n,
            remaining: (s?.remaining || 0) + n,
          }))
          setUploadLive(true)
          void pollUploadStatus()
        }
      })
    } finally {
      setBusy("")
    }
  }

  // 轮询上传队列状态；静默失败（不弹 Network Error 刷屏），完成后提示并停止轮询
  const pollUploadStatus = async () => {
    const resp = await r.get("/admin/thumb/upload_status").catch(() => null)
    if (!resp) return
    handleRespWithoutNotify(resp, (d) => {
      const s = d as {
        active: boolean
        paused: boolean
        queued: number
        remaining: number
        done: number
        failed: number
        exists: number
        fails: number
        total: number
        fail_items?: { path: string; msg: string }[]
      }
      setUpStatus({ ...upDefault, ...s })
      // 有运行中的上传（含暂停/风控暂停）时保持快轮询
      if (s.active || s.queued > 0 || s.remaining > 0) {
        setUploadLive(true)
      }
      if (uploadLive() && !s.active && s.remaining === 0 && s.total > 0) {
        notify.success(
          `上传完成：成功 ${s.done}，已存在(网盘已有) ${s.exists}，失败 ${s.failed}${s.fails > 0 ? "（可重试）" : ""}`,
        )
        loadTree()
        setUploadLive(false)
      }
    })
  }

  // 暂停上传队列（保留队列，可恢复）
  const uploadPause = async () => {
    setBusy("upload-pause")
    try {
      const resp = await r.post("/admin/thumb/upload/pause", {})
      handleResp(resp, () => void pollUploadStatus())
    } finally {
      setBusy("")
    }
  }

  // 恢复上传队列
  const uploadResume = async () => {
    setBusy("upload-resume")
    try {
      const resp = await r.post("/admin/thumb/upload/resume", {})
      handleResp(resp, () => void pollUploadStatus())
    } finally {
      setBusy("")
    }
  }

  // 删除上传队列（相当于停止上传，未上传的保留本地）
  const uploadClear = async () => {
    if (!window.confirm("确认删除上传队列？（未上传的缩略图保留在本地，可稍后重传）")) return
    setBusy("upload-clear")
    try {
      const resp = await r.post("/admin/thumb/upload/clear", {})
      handleResp(resp, () => {
        setUpStatus((s) => ({ ...upDefault, ...s, active: false, paused: false, queued: 0, remaining: 0 }))
        setUploadLive(false)
        void pollUploadStatus()
      })
    } finally {
      setBusy("")
    }
  }

  // 重试上传失败清单（超过自动重试次数的）
  const uploadRetry = async () => {
    setBusy("upload-retry")
    try {
      const resp = await r.post("/admin/thumb/upload_retry", {})
      handleResp(resp, (d) => {
        const data = d as { retried?: number }
        notify.success(`已重新入队 ${data.retried || 0} 个上传失败`)
        if (data.retried) {
          setUpStatus((s) => ({
            ...s,
            active: true,
            total: (s?.total || 0) + (data.retried || 0),
            remaining: (s?.remaining || 0) + (data.retried || 0),
          }))
          setUploadLive(true)
          void pollUploadStatus()
        }
      })
    } finally {
      setBusy("")
    }
  }

  const resetCandidateState = () => {
    candidateRequestId += 1
    candidateRequestActive = false
    const jobId = candJobId()
    if (jobId) {
      setCandJobId("")
      void r.post("/admin/thumb/candidates/cancel", { job_id: jobId }).catch(() => undefined)
    }
    setCands([])
    setCandSheet("")
    setRecommendedIndex(0)
    setCandCached(false)
    setCandRiskBlocked(false)
    setCandTruncated(false)
    setCandProgress(0)
    setCandLoading(false)
    setApplying(false)
  }

  const viewThumb = async (pp: string) => {
    if (viewUrl()) URL.revokeObjectURL(viewUrl())
    setViewUrl("")
    setViewPath(pp)
    setViewLoading(true)
    resetCandidateState()
    try {
      const resp = await r.get("/admin/thumb/view", {
        params: { path: pp },
        responseType: "blob",
      })
      if (resp instanceof Blob && resp.type.startsWith("image/")) {
        setViewUrl(URL.createObjectURL(resp))
      } else {
        notify.error("无法查看缩略图（未生成或生成失败）")
        setViewPath("")
      }
    } catch {
      notify.error("查看缩略图失败")
      setViewPath("")
    } finally {
      setViewLoading(false)
    }
  }

  const closeView = () => {
    if (viewUrl()) URL.revokeObjectURL(viewUrl())
    setViewUrl("")
    setViewPath("")
    setViewLoading(false)
    resetCandidateState()
  }

  const applyCandidateResponse = (
    data: ThumbCandidatesData,
    refresh: boolean,
    previousCandidates: ThumbCandidate[],
  ) => {
    const candidates = data.candidates || []
    if (refresh && !candidates.length && previousCandidates.length) {
      notify.warning("重新生成未取得新画面，已保留上次候选")
      return
    }
    setCands(candidates)
    setCandSheet(data.sheet || "")
    setRecommendedIndex(data.recommended_index || 0)
    setCandCached(!!data.cached)
    setCandRiskBlocked(!!data.risk_blocked)
    setCandTruncated(!!data.truncated)
    setCandProgress(100)
    if (!candidates.length) {
      if (data.risk_blocked || data.truncated) {
        notify.warning("为避免触发 115 风控，候选生成已停止，暂无可用画面")
      } else {
        notify.error("未能生成候选缩略图")
      }
    } else if (data.risk_blocked || data.truncated) {
      notify.warning(`已取得 ${candidates.length} 个画面，后续取帧已停止以避免触发 115 风控`)
    }
  }

  const pollCandidateJob = async (
    jobId: string,
    requestId: number,
    pp: string,
    refresh: boolean,
    previousCandidates: ThumbCandidate[],
  ) => {
    while (requestId === candidateRequestId && viewPath() === pp && candJobId() === jobId) {
      await new Promise((resolve) => setTimeout(resolve, 750))
      if (requestId !== candidateRequestId || viewPath() !== pp || candJobId() !== jobId) return
      try {
        const resp = await r.get("/admin/thumb/candidates/status", { params: { job_id: jobId } })
        let data: ThumbCandidatesData | undefined
        handleRespWithoutNotify(resp, (d) => {
          data = d as ThumbCandidatesData
        })
        if (!data) continue
        setCandProgress(data.progress || 0)
        if (data.state === "succeeded") {
          applyCandidateResponse(data, refresh, previousCandidates)
          setCandJobId("")
          setCandLoading(false)
          candidateRequestActive = false
          return
        }
        if (data.state === "failed" || data.state === "canceled") {
          setCandJobId("")
          setCandLoading(false)
          candidateRequestActive = false
          if (data.state === "failed") {
            notify.error(data.error || "生成候选缩略图失败")
          }
          return
        }
      } catch {
        if (requestId === candidateRequestId && viewPath() === pp) {
          notify.error("读取候选生成进度失败")
          setCandLoading(false)
          setCandJobId("")
        }
        candidateRequestActive = false
        return
      }
    }
  }

  // 生成候选缩略图：HTTP 只创建后台 job，前端轮询 1/9...9/9 进度，可随时取消。
  const loadCandidates = async (pp: string, refresh = false) => {
    if (candidateRequestActive) return
    candidateRequestActive = true
    const previousCandidates = cands()
    const requestId = ++candidateRequestId
    setCandLoading(true)
    setCandProgress(0)
    if (!refresh) {
      setCandSheet("")
      setRecommendedIndex(0)
      setCandCached(false)
      setCandRiskBlocked(false)
      setCandTruncated(false)
      setCands([])
    }
    try {
      const resp = await r.post("/admin/thumb/candidates", { path: pp, refresh })
      if (requestId !== candidateRequestId || viewPath() !== pp) return
      handleResp(resp, (d) => {
        if (requestId !== candidateRequestId || viewPath() !== pp) return
        const data = d as ThumbCandidatesData
        if (data.state === "succeeded" || data.candidates) {
          applyCandidateResponse(data, refresh, previousCandidates)
          setCandLoading(false)
          candidateRequestActive = false
          return
        }
        if (data.job_id) {
          setCandJobId(data.job_id)
          setCandProgress(data.progress || 0)
          void pollCandidateJob(data.job_id, requestId, pp, refresh, previousCandidates)
        }
      })
    } catch {
      if (requestId !== candidateRequestId || viewPath() !== pp) return
      notify.error(refresh && previousCandidates.length ? "重新生成失败，已保留上次候选" : "生成候选缩略图失败")
    } finally {
      if (requestId === candidateRequestId && viewPath() === pp && !candJobId()) {
        setCandLoading(false)
        candidateRequestActive = false
      }
    }
  }

  const openCandidates = (pp: string) => {
    if (candidateRequestActive) return
    if (viewUrl()) URL.revokeObjectURL(viewUrl())
    setViewPath(pp)
    setViewUrl("")
    setViewLoading(false)
    resetCandidateState()
    void loadCandidates(pp)
  }

  const cancelCandidates = async () => {
    const jobId = candJobId()
    if (!jobId) return
    candidateRequestId += 1
    candidateRequestActive = false
    setCandJobId("")
    setCandLoading(false)
    try {
      const resp = await r.post("/admin/thumb/candidates/cancel", { job_id: jobId })
      handleRespWithoutNotify(resp, () => notify.success("已取消候选生成"))
    } catch {
      // The job may already have completed between the last poll and the cancel click.
    }
  }
  // 保留所选候选缩略图；同一接口也用于保存九宫格
  const applyCandidate = async (pp: string, png: string, successMessage = "已应用所选缩略图") => {
    setApplying(true)
    try {
      const resp = await r.post("/admin/thumb/apply_candidate", { path: pp, png })
      handleResp(resp, () => {
        notify.success(successMessage)
        closeView()
        if (sel()) loadDir(sel())
        loadTree()
        load()
      })
    } finally {
      setApplying(false)
    }
  }

  const retryAll = async () => {
    const resp = await r.post("/admin/thumb/retry_fails", {})
    handleResp(resp, (d) => {
      const data = d as { retried?: number }
      notify.success(`已重试：${data.retried || 0} 个`)
      load()
    })
  }

  const pauseQueue = async () => {
    setBusy("queue-pause")
    try {
      const resp = await r.post("/admin/thumb/queue/pause", {})
      handleResp(resp, () => {
        notify.success("生成队列已暂停")
        load()
      })
    } finally {
      setBusy("")
    }
  }

  const resumeQueue = async () => {
    setBusy("queue-resume")
    try {
      const resp = await r.post("/admin/thumb/queue/resume", {})
      handleResp(resp, () => {
        notify.success("生成队列已恢复")
        load()
      })
    } finally {
      setBusy("")
    }
  }

  const clearQueue = async () => {
    if (!window.confirm("确认清空当前生成队列？（已入队的任务将被丢弃，可重新点生成）")) return
    setBusy("queue-clear")
    try {
      const resp = await r.post("/admin/thumb/queue/clear", {})
      handleResp(resp, (d) => {
        const data = d as { dropped?: number }
        notify.success(`已清空队列，丢弃 ${data.dropped || 0} 个任务`)
        setTotalQueued(0)
        load()
      })
    } finally {
      setBusy("")
    }
  }

  const deleteDir = async (pp: string) => {
    if (!window.confirm(`确认删除该目录的缩略图文件夹及本地缓存？（${pp}）`)) return
    setBusy("del-" + pp)
    try {
      const resp = await r.post("/admin/thumb/delete_folder", { path: pp })
      handleResp(resp, (d) => {
        const data = d as { removed?: number; folder?: string }
        notify.success(`已删除缩略图文件夹（${data.folder || ""}），清除 ${data.removed || 0} 个本地缩略图`)
        loadTree()
        if (sel() === pp) {
          setSelFiles([])
          setSelCount(0)
          setSelExcluded([])
          setChecked({})
        }
        load()
      })
    } finally {
      setBusy("")
    }
  }

  const retrySel = async () => {
    if (!sel()) {
      notify.warning("请先选择目录")
      return
    }
    const resp = await r.post("/admin/thumb/retry_fails", { path: sel() })
    handleResp(resp, (d) => {
      const data = d as { retried?: number }
      notify.success(`已重试：${data.retried || 0} 个`)
      load()
    })
  }

  const clearAll = async () => {
    if (
      !window.confirm(
        `确认清空本地缩略图缓存与索引？（本地 ${st()?.local_files || 0} 个将被删除；网盘上已上传的 ${st()?.cloud_files || 0} 个缩略图保留，不会删除）`,
      )
    )
      return
    setBusy("全部清空")
    try {
      const resp = await r.post("/admin/thumb/clear_all", {})
      handleResp(resp, (d) => {
        const data = d as { removed?: number }
        notify.success(`已清空 ${data.removed || 0} 个缩略图缓存`)
        loadTree()
        setSel("")
        setSelFiles([])
        setSelCount(0)
        load()
      })
    } finally {
      setBusy("")
    }
  }

  const toggleFile = (p: string) =>
    setChecked((cc) => {
      const z = { ...cc }
      z[p] = !z[p]
      return z
    })

  const excludeUnchecked = async () => {
    if (!sel()) {
      notify.warning("请先选择目录")
      return
    }
    const paths = selFiles().filter((p) => !checked()[p])
    if (!paths.length) {
      notify.warning("没有需要排除的视频")
      return
    }
    const resp = await r.post("/admin/thumb/exclude", { paths, exclude: true })
    handleResp(resp, () => {
      notify.success(`已排除 ${paths.length} 个视频`)
      loadDir(sel())
    })
  }

  const restoreExcluded = async () => {
    if (!sel()) {
      notify.warning("请先选择目录")
      return
    }
    const ex = selExcluded()
    if (!ex.length) {
      notify.warning("没有已排除的视频")
      return
    }
    const resp = await r.post("/admin/thumb/exclude", { paths: ex, exclude: false })
    handleResp(resp, () => {
      notify.success(`已恢复 ${ex.length} 个视频`)
      loadDir(sel())
    })
  }

  // 全排除：排除本目录全部媒体（不生成缩略图）
  const excludeAll = async () => {
    if (!sel() || !selFiles().length) {
      notify.warning("请先选择目录")
      return
    }
    if (!window.confirm(`确认排除本目录全部 ${selFiles().length} 个媒体？（${sel()}）`)) return
    const resp = await r.post("/admin/thumb/exclude", { paths: selFiles(), exclude: true })
    handleResp(resp, () => {
      notify.success(`已排除 ${selFiles().length} 个视频`)
      loadDir(sel())
    })
  }

  // 恢复全部纳入：取消本目录全部排除
  const restoreAll = async () => {
    if (!sel() || !selFiles().length) {
      notify.warning("请先选择目录")
      return
    }
    const resp = await r.post("/admin/thumb/exclude", { paths: selFiles(), exclude: false })
    handleResp(resp, () => {
      notify.success(`已恢复 ${selFiles().length} 个视频`)
      loadDir(sel())
    })
  }

  const migrate = async () => {
    if (!oldP() || !newP()) {
      notify.warning("请填写旧/新路径")
      return
    }
    const resp = await r.post("/admin/thumb/migrate", {
      old_prefix: oldP(),
      new_prefix: newP(),
    })
    handleResp(resp, (d) => {
      const data = d as { migrated?: number }
      notify.success(`已迁移 ${data.migrated || 0} 个缓存文件`)
      load()
      loadTree()
    })
  }

  const toggle = (pp: string) =>
    setExp((ss) => {
      const z = new Set(ss)
      z.has(pp) ? z.delete(pp) : z.add(pp)
      return z
    })

  const selectDir = (pp: string) => {
    setSel(pp)
    setExp((ss) => {
      const z = new Set(ss)
      z.add(pp)
      return z
    })
    loadDir(pp)
  }

  // 目录计数 Tag 颜色：网盘→绿，本地→橙，都有→灰
  const treeCachedColor = (nn: TreeNode) => {
    const total = nn.cached || 0
    if (total === 0) return "neutral"
    const l = nn.local || 0
    const c = nn.cloud || 0
    if (c > 0 && l === 0) return "success"
    if (l > 0 && c === 0) return "warning"
    return "neutral"
  }

  const TN = (nn: TreeNode, depth: number) => (
    <>
      <HStack
        spacing="$1"
        alignItems="center"
        w="$full"
        p="$2"
        rounded="$md"
        wrap="wrap"
        _hover={{ bgColor: useColorModeValue("$neutral2", "$neutral3")() }}
        background={
          sel() === nn.path
            ? useColorModeValue("$info2", "$info3")()
            : useColorModeValue("$neutral1", "$neutral2")()
        }
        style={{ "padding-left": `${10 + depth * 10}px`, cursor: "pointer" }}
        onClick={() => selectDir(nn.path)}
      >
        <Show when={(nn.children || []).length > 0} fallback={<Box w="$7" />}>
          <Button
            size="xs"
            variant="subtle"
            onClick={(e) => {
              e.stopPropagation()
              toggle(nn.path)
            }}
          >
            {expanded().has(nn.path) ? "▾" : "▸"}
          </Button>
        </Show>
        <Box mr="$1">📁</Box>
        <Box
          css={{
            flex: "1 1 auto",
            "font-size": "$sm",
            "white-space": "nowrap",
            overflow: "hidden",
            "text-overflow": "ellipsis",
          }}
          title={nn.name}
        >
          {nn.name}
        </Box>
        <Tag colorScheme={treeCachedColor(nn)}>{nn.cached}</Tag>
        <Show when={(nn.videos || 0) > nn.cached}>
          <Tag colorScheme="warning">缺 {nn.videos! - nn.cached}</Tag>
        </Show>
        <Button
          size="xs"
          disabled={busy() === nn.path}
          onClick={(e) => {
            e.stopPropagation()
            queueGen(nn.path, false)
          }}
        >
          生成
        </Button>
        <Button
          size="xs"
          colorScheme="info"
          disabled={busy() === "up-" + nn.path}
          onClick={(e) => {
            e.stopPropagation()
            uploadDir(nn.path)
          }}
        >
          上传
        </Button>
        <Button
          size="xs"
          colorScheme="danger"
          disabled={busy() === "del-" + nn.path}
          onClick={(e) => {
            e.stopPropagation()
            deleteDir(nn.path)
          }}
        >
          删除
        </Button>
      </HStack>
      <Show when={expanded().has(nn.path) && (nn.children || []).length > 0}>
        <For each={nn.children}>{(q) => TN(q, depth + 1)}</For>
      </Show>
    </>
  )

  // 先加载目录树（填充后端聚合统计），再刷新状态，保证顶部与树数字一致
  void loadTree().then(() => load())
  // 挂载时拉一次上传状态，恢复"正在上传 N"（若服务端有运行中则启动快轮询）
  void pollUploadStatus()
  // 10s 计时器仅刷新缩略图状态；upload_status 只在有上传运行时轮询，避免无意义的持续请求
  const timer = setInterval(() => {
    load()
  }, 10000)
  // 上传运行时 2s 快轮询，进度条实时跳动；空闲时完全不轮询 upload_status
  let fastTimer: ReturnType<typeof setInterval> | undefined
  createEffect(() => {
    if (uploadLive()) {
      fastTimer = setInterval(() => void pollUploadStatus(), 2000)
    } else if (fastTimer) {
      clearInterval(fastTimer)
      fastTimer = undefined
    }
  })
  onCleanup(() => {
    candidateRequestId += 1
    clearInterval(timer)
    if (fastTimer) clearInterval(fastTimer)
    if (treeRefreshTimer) clearTimeout(treeRefreshTimer)
    if (viewUrl()) URL.revokeObjectURL(viewUrl())
  })

  return (
    <VStack spacing="$3" alignItems="start" w="$full">
      <HStack spacing="$2" gap="$2" w="$full" wrap={{ "@initial": "wrap", "@md": "unset" }}>
        <Box
          p="$3"
          rounded="$lg"
          border="1px solid $neutral7"
          background={useColorModeValue("$neutral1", "$neutral2")()}
        >
          缓存 {st()?.cached_files || 0} 个
          <Text fontSize="$sm" color="$neutral9">
            网盘 {st()?.cloud_files || 0} · 本地 {st()?.local_files || 0}
          </Text>
        </Box>
        <Box
          p="$3"
          rounded="$lg"
          border="1px solid $neutral7"
          background={useColorModeValue("$neutral1", "$neutral2")()}
        >
          占用 {((st()?.cache_size || 0) / 1048576).toFixed(1)} MB
        </Box>
        <Show when={st()?.metrics}>
          <Box
            p="$3"
            rounded="$lg"
            border="1px solid $neutral7"
            background={useColorModeValue("$neutral1", "$neutral2")()}
          >
            命中率 {(st()?.metrics?.cache_hit_rate || 0).toFixed(1)}%
            <Text fontSize="$sm" color="$neutral9">
              生成均值 {st()?.metrics?.avg_generate_ms || 0}ms · P95 {st()?.metrics?.p95_generate_ms || 0}ms
            </Text>
            <Text fontSize="$xs" color="$neutral9">
              Range URL {st()?.metrics?.range_http || 0} · Reader {st()?.metrics?.range_reader || 0} · Gateway{" "}
              {st()?.metrics?.range_gateway || 0}
            </Text>
            <Show when={Object.keys(st()?.metrics?.failures || {}).length > 0}>
              <Text fontSize="$xs" color="$neutral9">
                失败分类{" "}
                {Object.entries(st()?.metrics?.failures || {})
                  .sort((a, b) => b[1] - a[1])
                  .map(([kind, count]) => `${kind} ${count}`)
                  .join(" · ")}
              </Text>
            </Show>
          </Box>
        </Show>
         <Show when={st()?.cache_dir}>
           <Box
             p="$3"
             rounded="$lg"
             border="1px solid $neutral7"
             background={useColorModeValue("$neutral1", "$neutral2")()}
           >
             缓存目录 {st()!.cache_dir}
           </Box>
         </Show>
        <Box
          p="$3"
          rounded="$lg"
          border="1px solid $neutral7"
          background={useColorModeValue("$neutral1", "$neutral2")()}
        >
          <VStack spacing="$1" alignItems="start">
            <HopeSwitch
              checked={!!st()?.prewarm_enabled}
              onChange={(e: Event) =>
                setAuto((e.currentTarget as HTMLInputElement).checked, undefined)
              }
            >
              自动生成
            </HopeSwitch>
            <HopeSwitch
              checked={!!st()?.auto_upload}
              onChange={(e: Event) =>
                setAuto(undefined, (e.currentTarget as HTMLInputElement).checked)
              }
            >
              自动上传
            </HopeSwitch>
          </VStack>
        </Box>
       </HStack>

      {/* 生成状态与控制 */}
      <Box mt="$2" rounded="$lg" border="1px solid $neutral6" p="$2" w="$full">
        <HStack spacing="$2" alignItems="center" wrap="wrap">
          <Tag
            colorScheme={
              genBlocked() || st()?.queue_paused
                ? "danger"
                : genActive() > 0
                  ? "success"
                  : queued() > 0
                    ? "warning"
                    : "neutral"
            }
          >
            {genBlocked()
              ? "部分 115 存储风控，相关生成暂停"
              : st()?.queue_paused
                ? "队列已暂停"
                : genActive() > 0
                  ? "正在生成中"
                  : queued() > 0
                    ? "已入队，等待生成"
                    : "空闲"}
          </Tag>
          <Text fontSize="$sm" color="$neutral9">
            {genActive() > 0 ? `正在生成 ${genActive()} 个 · ` : ""}队列剩余 {queued()} 个 · 本次已生成{" "}
            {Math.max(0, (st()?.cached_files || 0) - (baseCached() ?? 0))} 个 · 失败{" "}
            {st()?.fail_markers || 0} 个
          </Text>
           <Show when={(st()?.fail_markers || 0) > 0}>
             <Button
               size="xs"
               variant="outline"
               colorScheme="danger"
               onClick={() => openFailLog()}
             >
               查看失败日志
             </Button>
           </Show>
           <Button
             size="xs"
             colorScheme={st()?.queue_paused ? "success" : "warning"}
             disabled={busy() === "queue-pause" || busy() === "queue-resume"}
             onClick={() => (st()?.queue_paused ? resumeQueue() : pauseQueue())}
           >
             {st()?.queue_paused ? "恢复队列" : "暂停队列"}
           </Button>
           <Button
             size="xs"
             colorScheme="danger"
             disabled={busy() === "queue-clear" || !queued()}
             onClick={clearQueue}
           >
             删除队列
           </Button>
         </HStack>
        <Show when={(st()?.active_tasks || []).length > 0}>
          <Box mt="$2" rounded="$md" border="1px solid $neutral6" p="$1">
            <For each={st()!.active_tasks}>
              {(t) => (
                <Text fontSize="$xs" color="$neutral9" css={{ "word-break": "break-all" }}>
                  ▶ {t.path.split("/").pop()}
                </Text>
              )}
            </For>
          </Box>
        </Show>
        <Progress
          mt="$2"
          value={
            totalQueued() > 0
              ? Math.max(
                  0,
                  Math.min(100, Math.round(((totalQueued() - queued() - genActive()) / totalQueued()) * 100)),
                )
              : 0
          }
          max={100}
          indeterminate={genActive() > 0 && queued() + genActive() >= totalQueued()}
          size="sm"
        >
          <ProgressIndicator color="$info6" />
        </Progress>
      </Box>

      {/* 上传状态与进度（常驻，与生成面板一致） */}
      <Box mt="$2" rounded="$lg" border="1px solid $neutral6" p="$2" w="$full">
        <HStack spacing="$2" alignItems="center" wrap="wrap">
          <Tag
            colorScheme={
              upStatus().paused
                ? "warning"
                : st()?.blocked
                  ? "warning"
                  : upStatus().active
                    ? "success"
                    : upStatus().remaining > 0
                      ? "warning"
                      : "neutral"
            }
          >
            {upStatus().paused
              ? "上传已暂停"
              : st()?.blocked
                ? "部分 115 存储风控，相关上传暂停"
                : upStatus().active
                  ? "正在上传"
                  : upStatus().remaining > 0
                    ? "已入队，等待上传"
                    : upStatus().total > 0
                      ? "上传完成"
                      : "空闲"}
          </Tag>
          <Text fontSize="$sm" color="$neutral9">
            剩余 {upStatus().remaining} 个 · 成功 {upStatus().done} · 已存在(网盘已有){" "}
            {upStatus().exists} · 失败 {upStatus().failed}
          </Text>
          <Show when={upStatus().fails > 0}>
            <Button size="xs" variant="outline" colorScheme="danger" onClick={openUploadLog}>
              查看失败日志
            </Button>
            <Button
              size="xs"
              colorScheme="warning"
              loading={busy() === "upload-retry"}
              onClick={uploadRetry}
            >
              重试上传失败
            </Button>
          </Show>
          <Button
            size="xs"
            variant="outline"
            colorScheme={upStatus().paused ? "success" : "warning"}
            disabled={busy() === "upload-pause" || busy() === "upload-resume"}
            onClick={() => (upStatus().paused ? uploadResume() : uploadPause())}
          >
            {upStatus().paused ? "恢复上传" : "暂停上传"}
          </Button>
          <Button
            size="xs"
            variant="outline"
            colorScheme="danger"
            disabled={busy() === "upload-clear" || upStatus().total === 0}
            onClick={uploadClear}
          >
            删除上传队列
          </Button>
        </HStack>
        <Progress
          mt="$2"
          value={
            upStatus().total > 0
              ? Math.max(
                  0,
                  Math.min(
                    100,
                    Math.round(((upStatus().done + upStatus().exists) / upStatus().total) * 100),
                  ),
                )
              : 0
          }
          max={100}
          indeterminate={
            upStatus().active && upStatus().done + upStatus().exists + upStatus().failed === 0
          }
          size="sm"
        >
          <ProgressIndicator color="$success6" />
        </Progress>
      </Box>

      <HStack spacing="$2" alignItems="center" wrap={{ "@initial": "wrap", "@md": "unset" }}>
          <Button
            colorScheme="accent"
            loading={busy() === "upload-all"}
            onClick={uploadAll}
          >
            一键上传
          </Button>
          <Button colorScheme="warning" onClick={retryAll}>
            重试全部失败
          </Button>
          <Button colorScheme="danger" disabled={busy() === "全部清空"} onClick={clearAll}>
            清空全部缩略图
          </Button>
        <Text fontSize="$sm" color="$neutral10">
          点击目录查看已有缩略图，可勾选排除不需要缩略图的视频
        </Text>
      </HStack>

      <HStack
        direction={{ "@initial": "column", "@md": "row" }}
        spacing="$3"
        w="$full"
        alignItems="flex-start"
      >
        <Box
          w={{ "@initial": "$full", "@md": "50%" }}
          rounded="$lg"
          border="1px solid $neutral6"
          p="$1"
        >
          <Text fontWeight="$medium" p="$2">
            目录
          </Text>
          <Show when={treeScanStatus() === "refreshing"}>
            <HStack px="$2" pb="$2" spacing="$2">
              <Tag colorScheme="info" size="sm">后台校准中</Tag>
              <Text fontSize="$xs" color="$neutral9">当前先展示数据库快照，不阻塞页面</Text>
            </HStack>
          </Show>
          <Show when={treeLoading()}>
            <Text p="$2" fontSize="$sm" color="$neutral9">
              加载中...
            </Text>
          </Show>
          <VStack direction="column" w="$full">
            <For each={tree()}>{(q) => TN(q, 0)}</For>
          </VStack>
        </Box>
        <Box w={{ "@initial": "$full", "@md": "50%" }} rounded="$lg" border="1px solid $neutral6" p="$2">
          <Text fontWeight="$medium" css={{ "word-break": "break-all" }}>
            {sel() || "未选择目录"}
          </Text>
          <HStack spacing="$1" mt="$2" wrap="wrap">
            <Tag colorScheme="neutral">共有 {selFiles().length} 个媒体</Tag>
            <Tag colorScheme="info">已有缩略图 {selCount()} 个</Tag>
            <Show when={!dirListed()}>
              <Tag colorScheme="warning">列表受限（115 风控），仅展示有缩略图的文件</Tag>
            </Show>
            <Tag colorScheme={selExcluded().length ? "warning" : "neutral"}>
              已排除 {selExcluded().length} 个
            </Tag>
            <Button
              size="xs"
              colorScheme="accent"
              disabled={!sel() || busy() === "gen-" + sel()}
              onClick={genSelDir}
            >
              生成
            </Button>
            <Button
              size="xs"
              colorScheme="info"
              disabled={!sel() || busy() === "up-" + sel()}
              onClick={() => uploadDir(sel())}
            >
              上传
            </Button>
            <Button size="xs" colorScheme="warning" disabled={!sel()} onClick={retrySel}>
              重试失败
            </Button>
            <Button
              size="xs"
              colorScheme="danger"
              disabled={!sel() || busy() === "delpaths-" + sel()}
              onClick={deleteChecked}
            >
              删除
            </Button>
          </HStack>
          <HStack spacing="$2" alignItems="center" mt="$2" wrap="wrap">
            <Button size="xs" colorScheme="warning" disabled={!sel()} onClick={excludeUnchecked}>
              排除未勾选
            </Button>
            <Button size="xs" colorScheme="danger" disabled={!sel()} onClick={excludeAll}>
              全排除
            </Button>
            <Button size="xs" disabled={!sel() || !selExcluded().length} onClick={restoreExcluded}>
              恢复已排除
            </Button>
            <Button size="xs" colorScheme="success" disabled={!sel()} onClick={restoreAll}>
              恢复全部纳入
            </Button>
            <Text fontSize="$xs" color="$neutral9">
              勾选 = 纳入操作（生成/删除），取消勾选 = 排除
            </Text>
          </HStack>
          <Box mt="$2" maxH="420px" overflowY="auto" rounded="$md" border="1px solid $neutral6" p="$1">
            <For each={selFiles()}>
              {(q) => (
                <HStack
                  direction="row"
                  spacing="$2"
                  alignItems="center"
                  wrap="wrap"
                  p="$2"
                  rounded="$sm"
                  _hover={{ bgColor: useColorModeValue("$neutral2", "$neutral3")() }}
                >
                  <Button
                    size="xs"
                    variant="subtle"
                    colorScheme={checked()[q] ? "success" : "neutral"}
                    onClick={() => toggleFile(q)}
                  >
                    {checked()[q] ? "✓" : "○"}
                  </Button>
                  <Show
                    when={hasThumb()[q]}
                    fallback={
                      <Box
                        css={{
                          flex: "1 1 auto",
                          "word-break": "break-all",
                          "font-size": "$sm",
                          opacity: checked()[q] ? "1" : "0.5",
                          cursor: "pointer",
                        }}
                        title="无缩略图，点击选择画面"
                        onClick={() => openCandidates(q)}
                        _hover={{ color: "$info9" }}
                      >
                        {q.replace(sel(), "").replace(/^\//, "")}
                      </Box>
                    }
                  >
                    <Box
                      css={{
                        flex: "1 1 auto",
                        "word-break": "break-all",
                        "font-size": "$sm",
                        opacity: checked()[q] ? "1" : "0.5",
                        cursor: "pointer",
                      }}
                      title="点击查看缩略图"
                      onClick={() => viewThumb(q)}
                      _hover={{ color: "$info9" }}
                    >
                      {q.replace(sel(), "").replace(/^\//, "")}
                    </Box>
                  </Show>
                  <Show when={!hasThumb()[q]}>
                    <Tag colorScheme="neutral" size="sm">
                      无缩略图
                    </Tag>
                      <Button
                        size="xs"
                        variant="ghost"
                        colorScheme="info"
                        loading={candLoading() && viewPath() === q}
                        disabled={candLoading()}
                        onClick={() => openCandidates(q)}
                      >
                        选择画面
                      </Button>
                  </Show>
                  <Show when={failedMap()[q]}>
                    <Tag colorScheme="danger" size="sm" title={failedMap()[q] || "生成失败"}>
                      失败
                    </Tag>
                    <Button
                      size="xs"
                      variant="ghost"
                      colorScheme="danger"
                      onClick={() => openFailLog(q)}
                    >
                      日志
                    </Button>
                  </Show>
                  <Show when={!checked()[q]}>
                    <Tag colorScheme="warning" size="sm">
                      已排除
                    </Tag>
                  </Show>
                </HStack>
              )}
            </For>
          </Box>
        </Box>
      </HStack>

      <Show when={stale().length > 0}>
        <VStack spacing="$2" mt="$3" w="$full" direction="column">
          <Text fontWeight="$medium">检测到旧挂载路径（存储挂载路径已变更）：</Text>
          <Text fontSize="$sm">
            {stale().map((e) => e.dir + "（" + e.count + "）").join("、")}
          </Text>
          <HStack
            direction={{ "@initial": "column", "@md": "row" }}
            spacing="$2"
            alignItems="center"
          >
            <Text>旧前缀</Text>
            <Input
              value={oldP()}
              onInput={(e) => setOldP(e.currentTarget.value)}
              placeholder="如 /影视相关"
              w="$full"
              maxW="260px"
            />
            <Text>→ 新前缀</Text>
            <Input
              value={newP()}
              onInput={(e) => setNewP(e.currentTarget.value)}
              placeholder="如 /01_影视相关"
              w="$full"
              maxW="260px"
            />
            <Button colorScheme="info" onClick={migrate}>
              迁移
            </Button>
          </HStack>
        </VStack>
      </Show>

      <Modal
        opened={logOpen()}
        onClose={() => setLogOpen(false)}
        size={{ "@initial": "xs", "@md": "lg" }}
      >
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>失败日志（{logItems().length}）</ModalHeader>
          <ModalBody css={{ maxH: "60vh", overflow: "auto" }}>
            <Show when={logItems().length} fallback={<Text color="$neutral9">暂无失败记录</Text>}>
              <VStack spacing="$2" direction="column" w="$full">
                <For each={logItems()}>
                  {(it) => (
                    <Box
                      p="$2"
                      rounded="$md"
                      border="1px solid $neutral6"
                      w="$full"
                      background={useColorModeValue("$neutral1", "$neutral2")()}
                    >
                      <Text fontWeight="$medium" css={{ "word-break": "break-all" }}>
                        {it.path.split("/").pop()}
                      </Text>
                      <Text fontSize="$xs" color="$neutral9" css={{ "word-break": "break-all" }}>
                        {it.path}
                      </Text>
                      <Text fontSize="$sm" color="$danger9" css={{ "word-break": "break-all" }}>
                        原因：{it.msg}
                      </Text>
                      <Show when={it.at}>
                        <Text fontSize="$xs" color="$neutral9">
                          时间：{it.at}
                        </Text>
                      </Show>
                    </Box>
                  )}
                </For>
              </VStack>
            </Show>
          </ModalBody>
          <ModalFooter display="flex" gap="$2" justifyContent="flex-end">
            <Button
              colorScheme="danger"
              onClick={async () => {
                if (!window.confirm("确认删除全部失败日志？（不影响已生成的缩略图）")) return
                const resp = await r.post("/admin/thumb/clear_fails")
                handleResp(resp, (d) => {
                  setLogItems([])
                  void load()
                  notify.success(`已删除 ${(d as { removed?: number }).removed ?? 0} 条失败日志`)
                })
              }}
            >
              删除失败日志
            </Button>
            <Button colorScheme="neutral" onClick={() => setLogOpen(false)}>
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
      <Modal opened={!!viewPath()} onClose={closeView} size={{ "@initial": "sm", "@md": "md" }}>
        <ModalOverlay />
        <ModalContent>
          <ModalHeader css={{ "word-break": "break-all", "font-size": "$sm" }}>
            {viewPath().split("/").pop()}
          </ModalHeader>
          <ModalBody>
            <Show when={viewLoading()} fallback={<Box />}>
              <Text p="$4" fontSize="$sm" color="$neutral9">
                加载中…
              </Text>
            </Show>
            <Show when={viewUrl()}>
              <Box
                rounded="$md"
                border="1px solid $neutral6"
                overflow="hidden"
                background="#000"
                css={{ display: "flex", justifyContent: "center", alignItems: "center", maxH: "45vh" }}
              >
                <img
                  src={viewUrl()}
                  alt={viewPath()}
                  css={{ maxWidth: "100%", maxHeight: "45vh", objectFit: "contain" }}
                />
              </Box>
            </Show>
            <HStack mt="$2" spacing="$2" wrap="wrap">
              <Button
                size="xs"
                colorScheme="info"
                loading={candLoading()}
                disabled={!viewPath() || candLoading()}
                onClick={() => loadCandidates(viewPath(), cands().length > 0 || candCached())}
              >
                {cands().length || candCached() ? "重新生成候选" : "生成候选九宫格"}
              </Button>
              <Show when={candCached()}>
                <Tag colorScheme="neutral" size="sm">
                  已使用缓存
                </Tag>
              </Show>
              <Show when={candLoading() && candJobId()}>
                <Button size="xs" colorScheme="danger" variant="outline" onClick={cancelCandidates}>
                  取消候选生成
                </Button>
              </Show>
              <Text fontSize="$xs" color="$neutral9">
                115 安全模式：候选帧单路生成，检测到风控会立即停止
              </Text>
            </HStack>
            <Show when={candLoading() && candJobId()}>
              <Box mt="$2">
                <HStack spacing="$2" justifyContent="space-between">
                  <Text fontSize="$xs" color="$neutral9">
                    正在后台取帧 {Math.min(9, Math.ceil((candProgress() / 100) * 9))} / 9
                  </Text>
                  <Text fontSize="$xs" color="$neutral9">
                    {Math.round(candProgress())}%
                  </Text>
                </HStack>
                <Progress value={candProgress()} max={100} size="sm">
                  <ProgressIndicator color="$info6" />
                </Progress>
              </Box>
            </Show>
            <Show when={candSheet()}>
              <Box
                mt="$3"
                rounded="$md"
                border="1px solid $neutral6"
                p="$2"
                background={useColorModeValue("$neutral1", "$neutral2")()}
              >
                <Text fontSize="$sm" fontWeight="$medium">候选九宫格</Text>
                <img
                  src={`data:image/png;base64,${candSheet()}`}
                  alt="候选九宫格"
                  css={{ width: "100%", maxHeight: "30vh", objectFit: "contain", background: "#101010", display: "block", marginTop: "6px" }}
                />
                <HStack mt="$2" spacing="$2" wrap="wrap">
                  <Button
                    size="xs"
                    colorScheme="success"
                    loading={applying()}
                    disabled={candLoading()}
                    onClick={() => applyCandidate(viewPath(), candSheet(), "已应用九宫格缩略图")}
                  >
                    保存九宫格
                  </Button>
                  <Show when={recommendedIndex() > 0 && cands().some((cd) => cd.index === recommendedIndex())}>
                    <Button
                      size="xs"
                      colorScheme="accent"
                      loading={applying()}
                      disabled={candLoading()}
                      onClick={() => {
                        const recommended = cands().find((cd) => cd.index === recommendedIndex())
                        if (recommended) void applyCandidate(viewPath(), recommended.png, "已应用推荐缩略图")
                      }}
                    >
                      采用推荐画面
                    </Button>
                  </Show>
                  <Text fontSize="$xs" color="$neutral9">
                    已取得 {cands().length}/9 帧
                  </Text>
                </HStack>
                <Show when={candRiskBlocked() || candTruncated()}>
                  <Text mt="$1" fontSize="$xs" color="$warning9">
                    {candRiskBlocked()
                      ? "检测到 115 风控迹象，已停止后续取帧；请勿立即重复生成。"
                      : "候选生成未完整结束，已显示当前已取得的画面。"}
                  </Text>
                </Show>
              </Box>
            </Show>
            <Show when={cands().length}>
              <Grid
                mt="$3"
                w="$full"
                gap="$2"
                templateColumns={{ "@initial": "1fr", "@sm": "repeat(3, 1fr)" }}
              >
                <For each={cands()}>
                  {(cd) => (
                    <Box
                      rounded="$md"
                      border="1px solid $neutral6"
                      p="$1"
                      background={useColorModeValue("$neutral1", "$neutral2")()}
                    >
                      <img
                        src={`data:image/png;base64,${cd.png}`}
                        alt={`候选${cd.index}`}
                        css={{ width: "100%", borderRadius: "6px", display: "block" }}
                      />
                      <HStack mt="$1" spacing="$1" justifyContent="space-between" alignItems="center">
                        <HStack spacing="$1" alignItems="center">
                          <Text fontSize="$xs" color="$neutral9">
                            {Number(cd.at) >= 60
                              ? `${Math.floor(Number(cd.at) / 60)}m${Math.round(Number(cd.at) % 60)}s`
                              : `${cd.at}s`}
                          </Text>
                          <Show when={cd.index === recommendedIndex()}>
                            <Tag colorScheme="success" size="sm">推荐</Tag>
                          </Show>
                        </HStack>
                        <Button
                          size="xs"
                          colorScheme="success"
                          loading={applying()}
                          disabled={candLoading()}
                          onClick={() => applyCandidate(viewPath(), cd.png)}
                        >
                          保留此图
                        </Button>
                      </HStack>
                    </Box>
                  )}
                </For>
              </Grid>
            </Show>
          </ModalBody>
          <ModalFooter display="flex" gap="$2" justifyContent="flex-end">
            <Button colorScheme="neutral" onClick={closeView}>
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </VStack>
  )
}

export default Thumb
