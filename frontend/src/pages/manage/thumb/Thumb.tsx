import {
  Box,
  Button,
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
  Select,
  Tag,
  Text,
  VStack,
  useColorModeValue,
} from "@hope-ui/solid"
import { createSignal, For, Show, onCleanup } from "solid-js"
import { useManageTitle } from "~/hooks"
import { handleResp, notify, r } from "~/utils"
import { SelectOptions } from "~/components"

type ThumbStatus = {
  cached_files: number
  local_files: number
  cloud_files: number
  fail_markers: number
  cache_size: number
  cache_dir: string
  prewarm_queued: number
  queue_paused: boolean
  active_workers: number
  active_tasks?: { path: string; since: number }[]
  fail_items?: { path: string; dir: string; msg: string; at: string }[]
  blocked: boolean
  stale_by_dir?: { dir: string; count: number }[]
  mounts?: string[]
}

type TreeNode = {
  path: string
  name: string
  cached: number
  videos?: number
  children?: TreeNode[]
}

type ProxyNode = {
  id: number
  name: string
  type: string
  host: string
  port: number
  status: string
  fail_count: number
  risk_until?: string
  total_rx: number
  total_tx: number
  is_risk: boolean
  rx_rate: number
  tx_rate: number
  conns: number
  window_rx: number
  window_tx: number
}

type ThumbProxyConfig = {
  mode: string
  node_id: number
  effective?: { id: number; name: string; address: string; status: string }
  nodes: ProxyNode[]
  global_proxy_address: string
}

const fmtBytes = (n: number) => {
  if (!n || n <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let i = Math.floor(Math.log(n) / Math.log(1024))
  if (i >= units.length) i = units.length - 1
  return (n / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0) + " " + units[i]
}

const nodeStatusText = (n: ProxyNode) => {
  if (n.is_risk) return "风控中"
  if (n.status === "disabled") return "已停用"
  return "正常"
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
  const [pxCfg, setPxCfg] = createSignal<ThumbProxyConfig>()
  const [pxLoading, setPxLoading] = createSignal(false)
  const [viewPath, setViewPath] = createSignal("")
  const [viewUrl, setViewUrl] = createSignal("")
  const [viewLoading, setViewLoading] = createSignal(false)
  const [knownFails, setKnownFails] = createSignal<Set<string>>(new Set())
  const [failedMap, setFailedMap] = createSignal<Record<string, string>>({})
  let firstStatusLoaded = false
  const [upStatus, setUpStatus] = createSignal<{
    active: boolean
    queued: number
    done: number
    failed: number
    skipped: number
    total: number
  }>()
  let wasUpActive = false

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

  const loadProxy = async () => {
    const resp = await r.get("/admin/thumb/proxy")
    handleResp(resp, (d) => setPxCfg(d as ThumbProxyConfig))
  }

  const loadTree = async () => {
    setTreeLoading(true)
    try {
      const resp = await r.get("/admin/thumb/tree")
      handleResp(resp, (d) => {
        const data = d as { children?: TreeNode[] }
        setTree(data.children || [])
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
        }
        void pollUploadStatus()
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
        }
        void pollUploadStatus()
      })
    } finally {
      setBusy("")
    }
  }

  // 轮询上传队列状态；检测完成时提示并刷新
  const pollUploadStatus = async () => {
    const resp = await r.get("/admin/thumb/upload_status")
    handleResp(resp, (d) => {
      const s = d as {
        active: boolean
        queued: number
        done: number
        failed: number
        skipped: number
        total: number
      }
      setUpStatus(s)
      if (wasUpActive && !s.active && s.total > 0) {
        notify.success(`上传完成：成功 ${s.done}，跳过(已存在) ${s.skipped}，失败 ${s.failed}`)
        loadTree()
      }
      wasUpActive = !!s.active
    })
  }

  const viewThumb = async (pp: string) => {
    setViewPath(pp)
    setViewLoading(true)
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

  const saveProxy = async (mode: string, nodeID: number) => {
    setPxLoading(true)
    try {
      const resp = await r.post("/admin/thumb/proxy", { mode, node_id: nodeID })
      handleResp(resp, (d) => {
        setPxCfg(d as ThumbProxyConfig)
        notify.success("已保存缩略图代理配置")
      })
    } finally {
      setPxLoading(false)
    }
  }

  // 切换到手动指定：无有效节点时自动选第一个可用节点，没有节点则提示先去代理管理页添加
  const selectManual = () => {
    const usable = (pxCfg()?.nodes || []).filter((n) => n.status !== "disabled")
    if (!usable.length) {
      notify.warning("暂无可用代理节点，请先在「代理管理」页添加节点")
      return
    }
    const cur = pxCfg()?.node_id || 0
    const pick = usable.some((n) => n.id === cur) ? cur : usable[0].id
    saveProxy("manual", pick)
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

  const TN = (nn: TreeNode, depth: number) => (
    <>
      <HStack
        spacing="$1"
        alignItems="center"
        w="$full"
        p="$2"
        rounded="$md"
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
        <Tag colorScheme={sel() === nn.path ? "info" : "neutral"}>{nn.cached}</Tag>
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

  load()
  loadTree()
  loadProxy()
  const timer = setInterval(() => {
    load()
    void pollUploadStatus()
  }, 10000)
  onCleanup(() => clearInterval(timer))

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
          队列 {queued()} 个
        </Box>
        <Box
          p="$3"
          rounded="$lg"
          border="1px solid $neutral7"
          background={useColorModeValue("$neutral1", "$neutral2")()}
        >
          失败 {st()?.fail_markers || 0} 个
        </Box>
        <Box
          p="$3"
          rounded="$lg"
          border="1px solid $neutral7"
          background={useColorModeValue("$neutral1", "$neutral2")()}
        >
          占用 {((st()?.cache_size || 0) / 1048576).toFixed(1)} MB
        </Box>
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
        <HStack spacing="$2" alignItems="center">
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
      </HStack>

      {/* 代理节点选择 */}
      <Box mt="$2" rounded="$lg" border="1px solid $neutral6" p="$2" w="$full">
        <HStack spacing="$2" alignItems="center" wrap="wrap">
          <Text fontWeight="$medium">缩略图代理</Text>
          <Tag
            colorScheme={
              !pxCfg() || pxCfg().mode === "off" ? "neutral" : pxCfg().mode === "manual" ? "info" : "success"
            }
          >
            {!pxCfg() || pxCfg().mode === "off"
              ? "未启用"
              : pxCfg().mode === "manual"
                ? "手动指定"
                : "自动切换"}
          </Tag>
          <Show when={pxCfg()?.effective}>
            <Tag colorScheme="success">
              生效：{pxCfg()!.effective!.name}（{pxCfg()!.effective!.address}）
            </Tag>
          </Show>
          <Show when={!pxCfg()?.effective && pxCfg() && pxCfg().mode !== "off"}>
            <Tag colorScheme="warning">暂无可用节点</Tag>
          </Show>
        </HStack>
        <HStack
          spacing="$2"
          alignItems="center"
          wrap="wrap"
          mt="$2"
        >
          <Text fontSize="$sm">模式</Text>
          <Button
            size="xs"
            colorScheme={pxCfg()?.mode === "off" ? "accent" : "neutral"}
            onClick={() => saveProxy("off", 0)}
          >
            关闭（走全局代理）
          </Button>
          <Button
            size="xs"
            colorScheme={pxCfg()?.mode === "auto" ? "accent" : "neutral"}
            onClick={() => saveProxy("auto", pxCfg()?.node_id || 0)}
          >
            自动切换
          </Button>
          <Button
            size="xs"
            colorScheme={pxCfg()?.mode === "manual" ? "accent" : "neutral"}
            onClick={selectManual}
          >
            手动指定
          </Button>
          <Show when={pxCfg()?.mode === "manual"}>
            <Text fontSize="$sm" ml="$2">
              节点
            </Text>
            <Box w="$full" maxW="260px">
              <Select
                id="thumb-proxy-node"
                value={String(pxCfg()?.node_id || "")}
                onChange={(v) => saveProxy("manual", parseInt(v))}
              >
                <SelectOptions
                  options={(pxCfg()?.nodes || [])
                    .filter((n) => n.status !== "disabled")
                    .map((n) => ({
                      key: String(n.id),
                      label: `${n.name}（${n.host}:${n.port}）`,
                    }))}
                />
              </Select>
            </Box>
          </Show>
          <Show when={pxCfg()?.global_proxy_address}>
            <Text fontSize="$sm" color="$neutral9">
              全局代理：{pxCfg()!.global_proxy_address}
            </Text>
          </Show>
        </HStack>
        <Show when={(pxCfg()?.nodes || []).length > 0}>
          <Box mt="$2" maxH="260px" overflowY="auto" rounded="$md" border="1px solid $neutral6" p="$1">
            <For each={pxCfg()!.nodes}>
              {(n) => (
                <HStack
                  spacing="$2"
                  alignItems="center"
                  p="$2"
                  rounded="$sm"
                  _hover={{ bgColor: useColorModeValue("$neutral2", "$neutral3")() }}
                >
                  <Box css={{ flex: "1 1 auto", "word-break": "break-all", "font-size": "$sm" }}>
                    {n.name}（{n.host}:{n.port}）
                  </Box>
                  <Tag colorScheme={n.is_risk ? "danger" : n.status === "disabled" ? "neutral" : "success"}>
                    {nodeStatusText(n)}
                  </Tag>
                  <Show when={n.is_risk}>
                    <Tag colorScheme="warning">{Math.max(0, Math.ceil((new Date(n.risk_until!).getTime() - Date.now()) / 60000))} 分钟后自动恢复</Tag>
                  </Show>
                  <Text fontSize="$xs" color="$neutral9">
                    收 {fmtBytes(n.window_rx)} · 发 {fmtBytes(n.window_tx)} · 速率 {fmtBytes(n.rx_rate)}/s · 连接 {n.conns}
                  </Text>
                  <Tag colorScheme="neutral">
                    累计 {fmtBytes(n.total_rx)} / {fmtBytes(n.total_tx)}
                  </Tag>
                </HStack>
              )}
            </For>
          </Box>
        </Show>
      </Box>

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
              ? "115 风控中，生成已暂停"
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
            {Math.max(0, (st()?.cached_files || 0) - (baseCached() ?? 0))} 个
          </Text>
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
          <Show when={upStatus()?.active || (upStatus()?.queued ?? 0) > 0}>
            <Text fontSize="$sm" color="$info9">
              上传中：剩余 {upStatus()?.queued ?? 0} · 成功 {upStatus()?.done ?? 0} · 跳过{" "}
              {upStatus()?.skipped ?? 0} · 失败 {upStatus()?.failed ?? 0}
            </Text>
          </Show>
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
            <Button size="xs" disabled={!sel() || !selExcluded().length} onClick={restoreExcluded}>
              恢复已排除
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
                          opacity: "0.45",
                        }}
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
                  </Show>
                  <Show when={failedMap()[q]}>
                    <Tag colorScheme="danger" size="sm" title={failedMap()[q] || "生成失败"}>
                      失败
                    </Tag>
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
                css={{ display: "flex", justifyContent: "center", alignItems: "center", maxH: "70vh" }}
              >
                <img
                  src={viewUrl()}
                  alt={viewPath()}
                  css={{ maxWidth: "100%", maxHeight: "70vh", objectFit: "contain" }}
                />
              </Box>
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
