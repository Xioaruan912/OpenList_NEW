import {
  Box,
  Button,
  HStack,
  Input,
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
  fail_markers: number
  cache_size: number
  cache_dir: string
  prewarm_queued: number
  active_workers: number
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
  const [checked, setChecked] = createSignal<Record<string, boolean>>({})
  const [expanded, setExp] = createSignal<Set<string>>(new Set())
  const [treeLoading, setTreeLoading] = createSignal(false)
  const [stale, setStale] = createSignal<{ dir: string; count: number }[]>([])
  const [mounts, setMounts] = createSignal<string[]>([])
  const [oldP, setOldP] = createSignal("")
  const [newP, setNewP] = createSignal("")
  const [pxCfg, setPxCfg] = createSignal<ThumbProxyConfig>()
  const [pxLoading, setPxLoading] = createSignal(false)

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
      const data = d as { files?: string[]; count?: number; excluded?: string[] }
      setSelFiles(data.files || [])
      setSelCount(data.count || 0)
      setSelExcluded(data.excluded || [])
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

  const uploadDir = async (pp: string) => {
    setBusy("up-" + pp)
    try {
      const resp = await r.post("/admin/thumb/upload", { path: pp })
      handleResp(resp, (d) => {
        const data = d as { uploaded?: number; failed?: number; total?: number }
        if (!data.uploaded) {
          notify.info("该目录没有可上传的本地缩略图")
        } else {
          notify.success(
            `已上传 ${data.uploaded} 个缩略图到网盘 _thumbnails` +
              (data.failed ? `，失败 ${data.failed} 个` : ""),
          )
        }
        loadTree()
      })
    } finally {
      setBusy("")
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

  const clearSel = async () => {
    if (!sel()) {
      notify.warning("请先选择目录")
      return
    }
    if (!window.confirm(`确认清空该目录下所有缩略图？（${sel()}）`)) return
    setBusy(sel() + "-c")
    try {
      const resp = await r.post("/admin/thumb/clear", { path: sel() })
      handleResp(resp, (d) => {
        const data = d as { removed?: number; remote_skipped?: boolean }
        notify.success(
          `已清空 ${data.removed || 0} 个缩略图` +
            (data.remote_skipped ? "（115 风控中，远程缩略图待恢复后清理）" : ""),
        )
        loadTree()
        loadDir(sel())
        load()
      })
    } finally {
      setBusy("")
    }
  }

  const clearAll = async () => {
    if (
      !window.confirm(
        `确认清空全部缩略图缓存与索引？（已生成 ${st()?.cached_files || 0} 个缩略图将被删除，可重新生成）`,
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

  const findNode = (ns: TreeNode[], p: string): TreeNode | undefined => {
    for (const n of ns || []) {
      if (n.path === p) return n
      if (n.children?.length) {
        const f = findNode(n.children, p)
        if (f) return f
      }
    }
    return undefined
  }

  const selNode = () => findNode(tree(), sel())

  const TN = (nn: TreeNode, depth: number) => (
    <>
      <HStack
        spacing="$1"
        alignItems="center"
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
        <Box css={{ flex: "1 1 auto", "word-break": "break-all", "font-size": "$sm" }}>
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
              genBlocked() ? "danger" : genActive() > 0 ? "success" : queued() > 0 ? "warning" : "neutral"
            }
          >
            {genBlocked()
              ? "115 风控中，生成已暂停"
              : genActive() > 0
                ? "正在生成中"
                : queued() > 0
                  ? "已入队，等待生成"
                  : "空闲"}
          </Tag>
          <Text fontSize="$sm" color="$neutral9">
            队列剩余 {queued()} 个 · 本次已生成 {Math.max(0, (st()?.cached_files || 0) - (baseCached() ?? 0))} 个
          </Text>
        </HStack>
        <Progress
          mt="$2"
          value={totalQueued() > 0 ? Math.max(0, Math.min(100, Math.round(((totalQueued() - queued()) / totalQueued()) * 100))) : 0}
          max={100}
          indeterminate={genActive() > 0 && totalQueued() === 0}
          size="sm"
        >
          <ProgressIndicator color="$info6" />
        </Progress>
      </Box>

      <HStack spacing="$2" alignItems="center" wrap={{ "@initial": "wrap", "@md": "unset" }}>
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
            <Tag colorScheme="neutral">共有 {selNode()?.videos || selCount()} 个媒体</Tag>
            <Tag colorScheme="info">已有缩略图 {selCount()} 个</Tag>
            <Tag colorScheme={selExcluded().length ? "warning" : "neutral"}>
              已排除 {selExcluded().length} 个
            </Tag>
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
            <Button size="xs" colorScheme="danger" disabled={busy() === sel() + "-c"} onClick={clearSel}>
              清空
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
              取消勾选 = 不需要缩略图
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
                  <Box
                    css={{
                      flex: "1 1 auto",
                      "word-break": "break-all",
                      "font-size": "$sm",
                      opacity: checked()[q] ? "1" : "0.5",
                    }}
                  >
                    {q.replace(sel(), "").replace(/^\//, "")}
                  </Box>
                  <Show when={!checked()[q]}>
                    <Tag colorScheme="warning">已排除</Tag>
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
    </VStack>
  )
}

export default Thumb
