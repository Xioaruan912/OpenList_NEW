import { Button, HStack, Text, VStack } from "@hope-ui/solid"
import { createSignal, onCleanup } from "solid-js"
import { useManageTitle } from "~/hooks"
import { handleResp, handleRespWithoutNotify, notify } from "~/utils"
import { thumbApi } from "./api"
import { CandidateTaskCenter } from "./components/CandidateTaskCenter"
import { CandidateModal } from "./components/CandidateModal"
import { DirectoryDetail } from "./components/DirectoryDetail"
import { FailureLogModal } from "./components/FailureLogModal"
import { OverviewCards } from "./components/OverviewCards"
import { GenerationQueuePanel, UploadQueuePanel } from "./components/QueuePanels"
import { StalePathMigration } from "./components/StalePathMigration"
import { ThumbTreeView } from "./components/ThumbTreeView"
import { useCandidateController } from "./useCandidateController"
import {
  emptyUploadStatus,
  type ThumbRuntime,
  type ThumbStatus,
  type TreeNode,
  type UploadStatus,
} from "./types"

const Thumb = () => {
  useManageTitle("缩略图管理")
  const [st, setSt] = createSignal<ThumbStatus>()
  const [tree, setTree] = createSignal<TreeNode[]>([])
  const [queued, setQueued] = createSignal(0)
  const [genActive, setGenActive] = createSignal(0)
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
  const [oldP, setOldP] = createSignal("")
  const [newP, setNewP] = createSignal("")
  const [knownFails, setKnownFails] = createSignal<Set<string>>(new Set())
  const [failedMap, setFailedMap] = createSignal<Record<string, string>>({})
  let treeRefreshTimer: ReturnType<typeof setTimeout> | undefined
  const [treeScanStatus, setTreeScanStatus] = createSignal("")
  let firstStatusLoaded = false
  const [uploadLive, setUploadLive] = createSignal(false) // 本次会话是否有上传运行（控制轮询）
  const [upStatus, setUpStatus] = createSignal<UploadStatus>(emptyUploadStatus())
  const [logOpen, setLogOpen] = createSignal(false)
  const [logItems, setLogItems] = createSignal<{ path: string; msg: string; at: string }[]>([])
  const candidate = useCandidateController({
    selectedDir: sel,
    onApplied: () => {
      if (sel()) void loadDir(sel())
      void loadTree()
      void load()
    },
  })

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

  const clearFailureLogs = async () => {
    if (!window.confirm("确认删除全部失败日志？（不影响已生成的缩略图）")) return
    const resp = await thumbApi.clearFails()
    handleResp(resp, (d) => {
      setLogItems([])
      void load()
      notify.success(`已删除 ${(d as { removed?: number }).removed ?? 0} 条失败日志`)
    })
  }

  const load = async () => {
    const resp = await thumbApi.status()
    handleResp(resp, (d) => {
      const data = d as ThumbStatus
      setSt(data)
      setQueued(data.prewarm_queued || 0)
      if (baseCached() === null) setBaseCached(data.cached_files || 0)
      setStale(data.stale_by_dir || [])
      setOldP((data.stale_by_dir || [])[0]?.dir.split("/").slice(0, 2).join("/") || "")
      setNewP((data.mounts || [])[0] || "")
      setGenActive(data.active_workers || 0)
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
    const resp = await thumbApi.setAuto(generate, upload)
    handleResp(resp, () => void load())
  }

  const loadTree = async () => {
    if (treeRefreshTimer) {
      clearTimeout(treeRefreshTimer)
      treeRefreshTimer = undefined
    }
    if (!tree().length) setTreeLoading(true)
    try {
      const resp = await thumbApi.tree()
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
    const resp = await thumbApi.dir(pp)
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
      const resp = await thumbApi.generate(pp, true, !!force)
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
      const resp = await thumbApi.generate(sel(), false, false)
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
      const resp = await thumbApi.deletePaths(paths)
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
      const resp = await thumbApi.upload(pp)
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
      const resp = await thumbApi.uploadAll()
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

  // 高频控制面：只读内存状态，不扫本地目录、不访问网盘、不运行 ffmpeg。
  const pollRuntime = async () => {
    const resp = await thumbApi.runtime().catch(() => null)
    if (!resp) return
    handleRespWithoutNotify(resp, (d) => {
      const runtime = d as ThumbRuntime
      const generation = runtime.generation || {
        queued: 0,
        active: 0,
        paused: false,
        blocked: false,
        active_tasks: [],
      }
      const upload = { ...emptyUploadStatus(), ...(runtime.upload || {}) }
      setQueued(generation.queued || 0)
      setGenActive(generation.active || 0)
      setSt((current) =>
        current
          ? {
              ...current,
              prewarm_queued: generation.queued || 0,
              active_workers: generation.active || 0,
              queue_paused: !!generation.paused,
              blocked: !!generation.blocked,
              active_tasks: generation.active_tasks || [],
            }
          : current,
      )
      setUpStatus(upload)
      if (upload.active || upload.queued > 0 || upload.remaining > 0) {
        setUploadLive(true)
      }
      if (uploadLive() && !upload.active && upload.remaining === 0 && upload.total > 0) {
        notify.success(
          `上传完成：成功 ${upload.done}，已存在(网盘已有) ${upload.exists}，失败 ${upload.failed}${upload.fails > 0 ? "（可重试）" : ""}`,
        )
        void loadTree()
        setUploadLive(false)
      }
      candidate.applyJobs(runtime.candidate_jobs || [])
      if (runtime.tree?.scan_status) setTreeScanStatus(runtime.tree.scan_status)
    })
  }

  // 兼容现有业务动作调用；实际统一走控制面。
  const pollUploadStatus = pollRuntime

  // 暂停上传队列（保留队列，可恢复）
  const uploadPause = async () => {
    setBusy("upload-pause")
    try {
      const resp = await thumbApi.uploadPause()
      handleResp(resp, () => void pollUploadStatus())
    } finally {
      setBusy("")
    }
  }

  // 恢复上传队列
  const uploadResume = async () => {
    setBusy("upload-resume")
    try {
      const resp = await thumbApi.uploadResume()
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
      const resp = await thumbApi.uploadClear()
      handleResp(resp, () => {
        setUpStatus((s) => ({ ...emptyUploadStatus(), ...s, active: false, paused: false, queued: 0, remaining: 0 }))
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
      const resp = await thumbApi.uploadRetry()
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

  const retryAll = async () => {
    const resp = await thumbApi.retryFails()
    handleResp(resp, (d) => {
      const data = d as { retried?: number }
      notify.success(`已重试：${data.retried || 0} 个`)
      load()
    })
  }

  const pauseQueue = async () => {
    setBusy("queue-pause")
    try {
      const resp = await thumbApi.queuePause()
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
      const resp = await thumbApi.queueResume()
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
      const resp = await thumbApi.queueClear()
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
      const resp = await thumbApi.deleteFolder(pp)
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
    const resp = await thumbApi.retryFails(sel())
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
      const resp = await thumbApi.clearAll()
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
    const resp = await thumbApi.exclude(paths, true)
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
    const resp = await thumbApi.exclude(ex, false)
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
    const resp = await thumbApi.exclude(selFiles(), true)
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
    const resp = await thumbApi.exclude(selFiles(), false)
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
    const resp = await thumbApi.migrate(oldP(), newP())
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

  // 先加载目录树（填充后端聚合统计），再刷新状态，保证顶部与树数字一致
  void loadTree().then(() => load())
  // 轻量控制面恢复生成/上传/候选/Tree 运行状态。
  void pollRuntime()
  // 10s 只刷新重统计；2s 控制面只读取内存状态。
  const timer = setInterval(() => {
    load()
  }, 10000)
  const runtimeTimer = setInterval(() => void pollRuntime(), 2000)
  onCleanup(() => {
    clearInterval(timer)
    clearInterval(runtimeTimer)
    if (treeRefreshTimer) clearTimeout(treeRefreshTimer)
    candidate.dispose()
  })

  return (
    <VStack spacing="$3" alignItems="start" w="$full">
      <OverviewCards status={st()} onSetAuto={(generate, upload) => void setAuto(generate, upload)} />

      <GenerationQueuePanel
        status={st()}
        queued={queued()}
        active={genActive()}
        totalQueued={totalQueued()}
        baseCached={baseCached()}
        busy={busy()}
        onOpenFails={() => openFailLog()}
        onPause={() => void pauseQueue()}
        onResume={() => void resumeQueue()}
        onClear={() => void clearQueue()}
      />

      <UploadQueuePanel
        status={st()}
        upload={upStatus()}
        busy={busy()}
        onOpenFails={openUploadLog}
        onRetry={() => void uploadRetry()}
        onPause={() => void uploadPause()}
        onResume={() => void uploadResume()}
        onClear={() => void uploadClear()}
      />

      <CandidateTaskCenter
        jobs={candidate.jobs()}
        onOpen={(job) => void candidate.openJob(job)}
        onCancel={(job) => void candidate.cancelJob(job)}
      />

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
        <ThumbTreeView
          tree={tree()}
          selected={sel()}
          expanded={expanded()}
          loading={treeLoading()}
          scanStatus={treeScanStatus()}
          busy={busy()}
          onSelect={selectDir}
          onToggle={toggle}
          onGenerate={(path) => void queueGen(path, false)}
          onUpload={(path) => void uploadDir(path)}
          onDelete={(path) => void deleteDir(path)}
        />
        <DirectoryDetail
          selected={sel()}
          files={selFiles()}
          thumbCount={selCount()}
          excluded={selExcluded()}
          hasThumb={hasThumb()}
          listed={dirListed()}
          checked={checked()}
          failed={failedMap()}
          busy={busy()}
          candidateLoadingPath={candidate.loading() ? candidate.viewPath() : ""}
          onGenerate={() => void genSelDir()}
          onUpload={() => void uploadDir(sel())}
          onRetryFailed={() => void retrySel()}
          onDeleteChecked={() => void deleteChecked()}
          onExcludeUnchecked={() => void excludeUnchecked()}
          onExcludeAll={() => void excludeAll()}
          onRestoreExcluded={() => void restoreExcluded()}
          onRestoreAll={() => void restoreAll()}
          onToggleFile={toggleFile}
          onView={(path) => void candidate.view(path)}
          onCandidates={candidate.openGenerator}
          onFailLog={openFailLog}
        />
      </HStack>

      <StalePathMigration
        items={stale()}
        oldPrefix={oldP()}
        newPrefix={newP()}
        onOldPrefix={setOldP}
        onNewPrefix={setNewP}
        onMigrate={() => void migrate()}
      />

      <FailureLogModal
        opened={logOpen()}
        items={logItems()}
        onClose={() => setLogOpen(false)}
        onClear={() => void clearFailureLogs()}
      />
      <CandidateModal
        opened={!!candidate.viewPath()}
        path={candidate.viewPath()}
        viewUrl={candidate.viewUrl()}
        viewLoading={candidate.viewLoading()}
        loading={candidate.loading()}
        jobId={candidate.jobId()}
        progress={candidate.progress()}
        candidates={candidate.candidates()}
        sheet={candidate.sheet()}
        recommendedIndex={candidate.recommendedIndex()}
        cached={candidate.cached()}
        riskBlocked={candidate.riskBlocked()}
        truncated={candidate.truncated()}
        applying={candidate.applying()}
        onClose={candidate.close}
        onGenerate={(refresh) => void candidate.generate(candidate.viewPath(), refresh)}
        onCancel={() => void candidate.cancelCurrent()}
        onApply={(png, message) => void candidate.apply(candidate.viewPath(), png, message)}
      />
    </VStack>
  )
}

export default Thumb
