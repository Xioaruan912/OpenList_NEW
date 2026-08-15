import {
  Box,
  Button,
  FormControl,
  FormLabel,
  FormHelperText,
  HStack,
  Input,
  Modal,
  ModalBody,
  ModalCloseButton,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Select,
  Tag,
  Text,
  Textarea,
  VStack,
  useColorModeValue,
} from "@hope-ui/solid"
import { createSignal, For, onCleanup, Show } from "solid-js"
import { useManageTitle } from "~/hooks"
import { handleResp, handleRespWithNotifySuccess, notify, r } from "~/utils"
import { SelectOptions } from "~/components"

type ProxyNode = {
  id: number
  name: string
  type: string
  host: string
  port: number
  password: string
  traffic_port: number
  token: string
  remark: string
  status: string
  fail_count: number
  risk_until?: string
  total_rx: number
  total_tx: number
  last_used_at?: string
  is_risk: boolean
  rx_rate: number
  tx_rate: number
  conns: number
  window_rx: number
  window_tx: number
}

const fmtBytes = (n: number) => {
  if (!n || n <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let i = Math.floor(Math.log(n) / Math.log(1024))
  if (i >= units.length) i = units.length - 1
  return (n / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0) + " " + units[i]
}

const emptyForm = () => ({
  id: 0,
  name: "",
  type: "http",
  host: "",
  port: 1080,
  password: "",
  traffic_port: 0,
  token: "",
  remark: "",
})

const Proxy = () => {
  useManageTitle("代理管理")
  const [nodes, setNodes] = createSignal<ProxyNode[]>([])
  const [loading, setLoading] = createSignal(false)
  const [open, setOpen] = createSignal(false)
  const [editing, setEditing] = createSignal(false)
  const [form, setForm] = createSignal(emptyForm())
  const [saving, setSaving] = createSignal(false)

  const load = async () => {
    setLoading(true)
    try {
      const resp = await r.get("/admin/proxy/traffic")
      handleResp(resp, (d) => setNodes((d as ProxyNode[]) || []))
    } finally {
      setLoading(false)
    }
  }

  const openAdd = () => {
    setEditing(false)
    setForm(emptyForm())
    setOpen(true)
  }

  const openEdit = (n: ProxyNode) => {
    setEditing(true)
    setForm({
      id: n.id,
      name: n.name,
      type: n.type,
      host: n.host,
      port: n.port,
      password: n.password || "",
      traffic_port: n.traffic_port || 0,
      token: n.token || "",
      remark: n.remark || "",
    })
    setOpen(true)
  }

  const save = async () => {
    const f = form()
    if (!f.name || !f.host || !f.port) {
      notify.warning("请填写名称、地址与端口")
      return
    }
    setSaving(true)
    try {
      const resp = await r.post(
        `/admin/proxy/${editing() ? "update" : "create"}`,
        f,
      )
      handleRespWithNotifySuccess(resp, () => {
        setOpen(false)
        load()
      })
    } finally {
      setSaving(false)
    }
  }

  const del = async (n: ProxyNode) => {
    if (!window.confirm(`确认删除节点 ${n.name}？`)) return
    const resp = await r.post("/admin/proxy/delete", { id: n.id })
    handleResp(resp, () => {
      notify.success("已删除")
      load()
    })
  }

  const recover = async (id: number) => {
    const resp = await r.post("/admin/proxy/recover", { id })
    handleResp(resp, () => {
      notify.success("已解除风控")
      load()
    })
  }

  const setStatus = async (n: ProxyNode, status: string) => {
    const resp = await r.post("/admin/proxy/enable", { id: n.id, status })
    handleResp(resp, () => {
      notify.success(status === "disabled" ? "已停用" : "已启用")
      load()
    })
  }

  load()
  const timer = setInterval(() => load(), 10000)
  onCleanup(() => clearInterval(timer))

  const nodeAddress = (n: ProxyNode) =>
    `${n.type}://proxy:${n.password ? "****" : ""}@${n.host}:${n.port}`

  return (
    <VStack spacing="$3" alignItems="start" w="$full">
      <HStack spacing="$2" alignItems="center">
        <Button colorScheme="accent" loading={loading()} onClick={load}>
          刷新
        </Button>
        <Button onClick={openAdd}>添加节点</Button>
        <Text fontSize="$sm" color="$neutral10">
          节点用于 115 缩略图下载分散出口 IP；支持 http 与 ss（按 http 方式连接）代理
        </Text>
      </HStack>
      <VStack spacing="$2" w="$full" direction="column">
        <For each={nodes()}>
          {(n) => (
            <HStack
              spacing="$2"
              alignItems="center"
              w="$full"
              p="$2"
              rounded="$md"
              border="1px solid $neutral6"
              background={useColorModeValue("$neutral1", "$neutral2")()}
              wrap={{ "@initial": "wrap", "@md": "unset" }}
            >
              <Box css={{ flex: "1 1 auto", "word-break": "break-all" }}>
                <HStack spacing="$1">
                  <Text fontWeight="$medium">{n.name}</Text>
                  <Show when={n.type === "ss"}>
                    <Tag colorScheme="info">ss</Tag>
                  </Show>
                </HStack>
                <Text fontSize="$sm" color="$neutral9">
                  {n.host}:{n.port} · {nodeAddress(n)}
                </Text>
                <Show when={n.remark}>
                  <Text fontSize="$xs" color="$neutral9">
                    {n.remark}
                  </Text>
                </Show>
              </Box>
              <Tag
                colorScheme={
                  n.is_risk ? "danger" : n.status === "disabled" ? "neutral" : "success"
                }
              >
                {n.is_risk ? "风控中" : n.status === "disabled" ? "已停用" : "正常"}
              </Tag>
              <Show when={n.is_risk && n.risk_until}>
                <Tag colorScheme="warning">
                  {Math.max(0, Math.ceil((new Date(n.risk_until!).getTime() - Date.now()) / 60000))} 分钟后恢复
                </Tag>
              </Show>
              <Text fontSize="$xs" color="$neutral9" css={{ "text-align": "right" }}>
                收 {fmtBytes(n.total_rx)} · 发 {fmtBytes(n.total_tx)}
                <br />
                速率 {fmtBytes(n.rx_rate)}/s · 连接 {n.conns}
              </Text>
              <Show when={n.is_risk}>
                <Button size="xs" colorScheme="warning" onClick={() => recover(n.id)}>
                  解除风控
                </Button>
              </Show>
              <Button
                size="xs"
                onClick={() => setStatus(n, n.status === "disabled" ? "normal" : "disabled")}
              >
                {n.status === "disabled" ? "启用" : "停用"}
              </Button>
              <Button size="xs" colorScheme="accent" onClick={() => openEdit(n)}>
                编辑
              </Button>
              <Button size="xs" colorScheme="danger" onClick={() => del(n)}>
                删除
              </Button>
            </HStack>
          )}
        </For>
        <Show when={!loading() && nodes().length === 0}>
          <Text color="$neutral9">暂无代理节点，点击「添加节点」创建</Text>
        </Show>
      </VStack>

      <Modal opened={open()} onClose={() => setOpen(false)} size={{ "@initial": "xs", "@md": "md" }}>
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>{editing() ? "编辑节点" : "添加节点"}</ModalHeader>
          <ModalCloseButton />
          <ModalBody>
            <VStack spacing="$2" alignItems="start" w="$full">
              <FormControl w="$full" required>
                <FormLabel>名称</FormLabel>
                <Input
                  value={form().name}
                  onInput={(e) => setForm((f) => ({ ...f, name: e.currentTarget.value }))}
                  placeholder="如 vps-hk"
                />
                <FormHelperText>用于识别，如 vps-hk / vps-jp</FormHelperText>
              </FormControl>
              <HStack spacing="$2" w="$full" alignItems="flex-end">
                <FormControl w="$full" required>
                  <FormLabel>类型</FormLabel>
                  <Select
                    value={form().type}
                    onChange={(v) => setForm((f) => ({ ...f, type: v }))}
                  >
                    <SelectOptions
                      options={[
                        { key: "http", label: "http" },
                        { key: "ss", label: "ss" },
                      ]}
                    />
                  </Select>
                </FormControl>
                <FormControl w="$full" required>
                  <FormLabel>地址</FormLabel>
                  <Input
                    value={form().host}
                    onInput={(e) => setForm((f) => ({ ...f, host: e.currentTarget.value }))}
                    placeholder="IP 或域名"
                  />
                </FormControl>
                <FormControl w="130px" required>
                  <FormLabel>端口</FormLabel>
                  <Input
                    type="number"
                    value={String(form().port)}
                    onInput={(e) => setForm((f) => ({ ...f, port: parseInt(e.currentTarget.value) || 0 }))}
                  />
                </FormControl>
              </HStack>
              <FormControl w="$full">
                <FormLabel>密码</FormLabel>
                <Input
                  type="password"
                  value={form().password}
                  onInput={(e) => setForm((f) => ({ ...f, password: e.currentTarget.value }))}
                  placeholder="代理鉴权密码（留空不修改）"
                />
                <FormHelperText>
                  部署脚本用户名为 proxy，地址格式 http://proxy:密码@地址:端口
                </FormHelperText>
              </FormControl>
              <FormControl w="$full">
                <FormLabel>备注</FormLabel>
                <Textarea
                  value={form().remark}
                  onInput={(e) => setForm((f) => ({ ...f, remark: e.currentTarget.value }))}
                />
              </FormControl>
            </VStack>
          </ModalBody>
          <ModalFooter display="flex" gap="$2" mt="$3" justifyContent="flex-end">
            <Button colorScheme="neutral" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button colorScheme="accent" loading={saving()} onClick={save}>
              {editing() ? "保存" : "添加"}
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </VStack>
  )
}

export default Proxy