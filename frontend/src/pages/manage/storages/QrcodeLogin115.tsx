import {
  Button,
  Divider,
  Image,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Radio,
  RadioGroup,
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
  Spinner,
  Stack,
  Text,
  VStack,
} from "@hope-ui/solid"
import { createSignal, For, onCleanup, Show } from "solid-js"
import { notify, r } from "~/utils"
import { PResp } from "~/types"

interface QRCodeInfo {
  uid: string
  time: number
  sign: string
  qrcode: string
}

interface QRCodeStatusInfo {
  status: number
  msg: string
}

interface FolderInfo {
  file_id: string
  parent_id: string
  name: string
}

const LOGIN_APPS = [
  { key: "web", label: "网页端 (web)" },
  { key: "android", label: "安卓客户端 (android)" },
  { key: "ios", label: "苹果客户端 (ios)" },
  { key: "tv", label: "电视端 (tv)" },
  { key: "alipaymini", label: "支付宝小程序 (alipaymini)" },
  { key: "wechatmini", label: "微信小程序 (wechatmini)" },
  { key: "qandroid", label: "安卓多开 (qandroid)" },
]

const STATUS_TEXT: Record<number, string> = {
  0: "等待扫码...",
  1: "已扫码，请在手机上确认登录",
  2: "登录成功",
  [-1]: "二维码已过期，请刷新",
  [-2]: "已取消登录",
}

export const QrcodeLogin115 = (props: {
  setAddition: (name: string, value: any) => void
}) => {
  const [app, setApp] = createSignal("web")
  const [qrData, setQrData] = createSignal<QRCodeInfo | null>(null)
  const [qrStatus, setQrStatus] = createSignal(0)
  const [polling, setPolling] = createSignal(false)
  const [logging, setLogging] = createSignal(false)
  const [folderModal, setFolderModal] = createSignal(false)
  const [folders, setFolders] = createSignal<FolderInfo[]>([])
  const [selectedId, setSelectedId] = createSignal("")
  const [loadingFolders, setLoadingFolders] = createSignal(false)

  let timer: ReturnType<typeof setInterval> | null = null

  const stopPolling = () => {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
    setPolling(false)
  }

  onCleanup(() => stopPolling())

  const fetchQRCode = async () => {
    stopPolling()
    setQrData(null)
    const resp: PResp<QRCodeInfo> = r.get("/115/qrcode")
    const { code, message, data } = await resp
    if (code !== 200) {
      notify.error(message)
      return
    }
    setQrData(data)
    setQrStatus(0)
    setPolling(true)
    timer = setInterval(pollStatus, 2000)
  }

  const pollStatus = async () => {
    const data = qrData()
    if (!data) return
    const resp: PResp<QRCodeStatusInfo> = r.get("/115/qrcode/status", {
      params: { uid: data.uid, time: data.time, sign: data.sign },
    })
    const { code, message, data: statusData } = await resp
    if (code !== 200) {
      stopPolling()
      notify.error(message)
      return
    }
    const status = statusData.status
    setQrStatus(status)
    if (status === 2) {
      stopPolling()
      await login()
    } else if (status < 0) {
      stopPolling()
    }
  }

  const login = async () => {
    const data = qrData()
    if (!data) return
    setLogging(true)
    const resp: PResp<{ cookie: string }> = r.post("/115/qrcode/login", {
      uid: data.uid,
      app: app(),
    })
    const { code, message, data: loginData } = await resp
    setLogging(false)
    if (code !== 200) {
      notify.error(message)
      return
    }
    const cookie = loginData.cookie
    props.setAddition("cookie", cookie)
    notify.success("扫码登录成功，Cookie 已自动填入")
    await openFolderPicker(cookie)
  }

  const openFolderPicker = async (cookie: string) => {
    setLoadingFolders(true)
    setFolderModal(true)
    const resp: PResp<{ content: FolderInfo[] }> = r.post("/115/root_folders", {
      cookie,
    })
    const { code, message, data } = await resp
    setLoadingFolders(false)
    if (code !== 200) {
      notify.error(message)
      setFolderModal(false)
      return
    }
    setFolders(data.content || [])
    setSelectedId("")
  }

  const confirmFolder = () => {
    const id = selectedId()
    if (!id) {
      notify.warning("请选择一个文件夹")
      return
    }
    props.setAddition("root_folder_id", id)
    setFolderModal(false)
    const folder = folders().find((f) => f.file_id === id)
    notify.success(`已选择挂载文件夹: ${folder?.name ?? id}`)
  }

  return (
    <VStack w="$full" spacing="$2" alignItems="flex-start" border="1px solid $neutral5" borderRadius="$md" p="$3">
      <Text fontWeight="$semibold" size="lg">
        115 扫码登录（免手动获取 Cookie）
      </Text>
      <Text size="sm" color="$neutral9">
        选择要绑定的设备后获取二维码，使用 115 App 扫码，登录成功后 Cookie 将自动填入，并弹出挂载文件夹选择框。
      </Text>
      <Stack direction={{ "@initial": "column", "@sm": "row" }} spacing="$2">
        <Select value={app()} onChange={(v: any) => setApp(v)} aria-label="login app">
          <SelectTrigger>
            <SelectPlaceholder>选择设备</SelectPlaceholder>
            <SelectValue />
            <SelectIcon />
          </SelectTrigger>
          <SelectContent>
            <SelectListbox>
              <For each={LOGIN_APPS}>
                {(item) => (
                  <SelectOption value={item.key}>
                    <SelectOptionText>{item.label}</SelectOptionText>
                    <SelectOptionIndicator />
                  </SelectOption>
                )}
              </For>
            </SelectListbox>
          </SelectContent>
        </Select>
        <Button onClick={fetchQRCode} disabled={polling() || logging()}>
          {qrData() ? "刷新二维码" : "获取二维码"}
        </Button>
        <Show when={polling()}>
          <Button variant="outline" onClick={stopPolling}>
            停止轮询
          </Button>
        </Show>
      </Stack>
      <Show when={qrData()}>
        <VStack spacing="$1">
          <Image
            src={`data:image/png;base64,${qrData()!.qrcode}`}
            w="$56"
            h="$56"
            alt="115 login qrcode"
          />
          <Show
            when={logging()}
            fallback={
              <Text
                size="sm"
                color={
                  qrStatus() === 2
                    ? "$success9"
                    : qrStatus() < 0
                      ? "$danger9"
                      : "$neutral8"
                }
              >
                {STATUS_TEXT[qrStatus()] ?? "未知状态"}
              </Text>
            }
          >
            <Text size="sm" color="$warning9">
              正在获取登录 Cookie...
            </Text>
          </Show>
        </VStack>
      </Show>
      <Divider />
      <Text size="sm" color="$neutral9">
        提示：请选择平时不常用的设备登录，否则会挤掉该设备上的原有登录。获取 Cookie 后会自动弹出挂载文件夹选择框。
      </Text>

      <Modal blockScrollOnMount={false} opened={folderModal()} onClose={() => setFolderModal(false)}>
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>选择要挂载的文件夹</ModalHeader>
          <ModalBody>
            <Show when={loadingFolders()} fallback={<Text>选择需要挂载的文件夹，将自动填入根文件夹 ID</Text>}>
              <VStack spacing="$2">
                <Spinner />
                <Text size="sm">正在读取 115 网盘根目录...</Text>
              </VStack>
            </Show>
            <Show when={!loadingFolders() && folders().length === 0}>
              <Text color="$danger9">未获取到文件夹列表，请确认 Cookie 是否有效</Text>
            </Show>
            <Show when={!loadingFolders() && folders().length > 0}>
              <RadioGroup value={selectedId()} onChange={setSelectedId}>
                <Stack direction="column" spacing="$1" maxHeight="$80" overflow="auto">
                  <For each={folders()}>
                    {(folder) => (
                      <Radio value={folder.file_id}>
                        {folder.name} ({folder.file_id})
                      </Radio>
                    )}
                  </For>
                </Stack>
              </RadioGroup>
            </Show>
          </ModalBody>
          <ModalFooter display="flex" gap="$2">
            <Button onClick={() => setFolderModal(false)} colorScheme="neutral">
              取消
            </Button>
            <Button onClick={confirmFolder} disabled={!selectedId()}>
              确定
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </VStack>
  )
}
