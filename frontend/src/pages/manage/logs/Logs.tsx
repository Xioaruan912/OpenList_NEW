import {
  Box,
  Button,
  HStack,
  Select,
  SelectContent,
  SelectIcon,
  SelectListbox,
  SelectOption,
  SelectOptionIndicator,
  SelectOptionText,
  SelectPlaceholder,
  SelectTrigger,
  SelectValue,
  Tag,
  Text,
  VStack,
  Switch as HopeSwitch,
} from "@hope-ui/solid"
import { createSignal, For, onCleanup, Show } from "solid-js"
import { useManageTitle } from "~/hooks"
import { handleResp, r } from "~/utils"
import { Storage } from "~/types"

interface ActivityEntry {
  at: string
  level: "success" | "error" | "warn"
  action: string
  mount: string
  path?: string
  msg: string
}

const actionText: Record<string, string> = {
  upload_success: "上传成功",
  sec_upload: "秒传成功",
  upload_fail: "上传失败",
  storage_error: "存储错误",
  blocked: "风控标记",
  unblocked: "风控解除",
}

const levelText: Record<string, string> = {
  success: "成功",
  error: "失败",
  warn: "警告",
}

const levelColor: Record<string, string> = {
  success: "success",
  error: "danger",
  warn: "warning",
}

const fmtTime = (at: string) => {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(
    d.getHours(),
  )}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

const Logs = () => {
  useManageTitle("manage.sidemenu.activity_log")
  const [storages, setStorages] = createSignal<Storage[]>([])
  const [mount, setMount] = createSignal("")
  const [logs, setLogs] = createSignal<ActivityEntry[]>([])
  const [auto, setAuto] = createSignal(false)

  const loadStorages = async () => {
    const resp = await r.get("/admin/storage/list")
    handleResp(resp, (data) => setStorages(data.content))
  }
  const loadLogs = async () => {
    const resp = await r.get("/admin/storage/logs", {
      params: { mount: mount() || undefined, limit: 20 },
    })
    handleResp(resp, (data) => setLogs(data.content || []))
  }
  loadStorages()
  loadLogs()

  let timer: ReturnType<typeof setInterval> | undefined
  const toggleAuto = (on: boolean) => {
    setAuto(on)
    if (timer) clearInterval(timer)
    if (on) {
      timer = setInterval(loadLogs, 5000)
    }
  }
  onCleanup(() => {
    if (timer) clearInterval(timer)
  })

  return (
    <VStack spacing="$3" alignItems="start" w="$full">
      <HStack spacing="$2" gap="$2" w="$full" wrap="wrap">
        <Button colorScheme="accent" onClick={loadLogs}>
          刷新
        </Button>
        <HopeSwitch checked={auto()} onChange={(e: Event) => toggleAuto((e.currentTarget as HTMLInputElement).checked)}>
          自动刷新（5 秒）
        </HopeSwitch>
        <Select value={mount()} onChange={setMount}>
          <SelectTrigger>
            <SelectPlaceholder>全部存储</SelectPlaceholder>
            <SelectValue />
            <SelectIcon />
          </SelectTrigger>
          <SelectContent>
            <SelectListbox>
              <SelectOption value="">
                <SelectOptionText>全部存储</SelectOptionText>
                <SelectOptionIndicator />
              </SelectOption>
              <For each={storages()}>
                {(item) => (
                  <SelectOption value={item.mount_path}>
                    <SelectOptionText>
                      {item.mount_path}
                      {item.driver ? ` (${item.driver})` : ""}
                    </SelectOptionText>
                    <SelectOptionIndicator />
                  </SelectOption>
                )}
              </For>
            </SelectListbox>
          </SelectContent>
        </Select>
        <Text size="sm" color="$neutral9">
          最近 {logs().length} 条活动记录
        </Text>
      </HStack>
      <Box w="$full" css={{ maxH: "calc(100vh - 180px)", overflow: "auto" }}>
        <Show when={logs().length} fallback={<Text color="$neutral9">暂无活动记录</Text>}>
          <VStack spacing="$2" direction="column" w="$full">
            <For each={logs()}>
              {(it) => (
                <Box
                  p="$2"
                  rounded="$md"
                  border="1px solid $neutral6"
                  w="$full"
                  background="$background"
                >
                  <HStack spacing="$2" alignItems="center">
                    <Tag colorScheme={levelColor[it.level]} size="sm">
                      {levelText[it.level] || it.level}
                    </Tag>
                    <Text fontWeight="$medium" size="sm">
                      {actionText[it.action] || it.action}
                    </Text>
                    <Text fontSize="$xs" color="$neutral9">
                      {it.mount}
                    </Text>
                    <Text fontSize="$xs" color="$neutral9" ml="auto">
                      {fmtTime(it.at)}
                    </Text>
                  </HStack>
                  <Show when={it.path}>
                    <Text fontSize="$xs" color="$neutral8" css={{ "word-break": "break-all" }}>
                      {it.path}
                    </Text>
                  </Show>
                  <Text fontSize="$sm" css={{ "word-break": "break-all" }}>
                    {it.msg}
                  </Text>
                </Box>
              )}
            </For>
          </VStack>
        </Show>
      </Box>
    </VStack>
  )
}

export default Logs