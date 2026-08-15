import {
  Box,
  Button,
  HStack,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Radio,
  RadioGroup,
  Spinner,
  Stack,
  Tag,
  Text,
  VStack,
} from "@hope-ui/solid"
import { createEffect, createSignal, For, Show } from "solid-js"
import type { Accessor } from "solid-js"
import { notify, r } from "~/utils"
import { PResp } from "~/types"

interface FolderInfo {
  file_id: string
  parent_id: string
  name: string
}

interface FolderCrumb {
  file_id: string
  name: string
}

// FolderPicker115 可逐级浏览 115 网盘目录的可视化挂载文件夹选择器。
// 用 cookie 拉取根目录/子目录，支持进入子文件夹与返回上级。
export const FolderPicker115 = (props: {
  opened: Accessor<boolean>
  cookie: Accessor<string>
  onClose: () => void
  onPick: (fileId: string, name: string) => void
}) => {
  const [loading, setLoading] = createSignal(false)
  const [folders, setFolders] = createSignal<FolderInfo[]>([])
  const [crumbs, setCrumbs] = createSignal<FolderCrumb[]>([])
  const [selectedId, setSelectedId] = createSignal("")

  const currentId = () =>
    crumbs().length > 0 ? crumbs()[crumbs().length - 1].file_id : ""

  const loadFolders = async () => {
    const cookie = props.cookie()
    if (!cookie) {
      notify.warning("请先填写或获取 115 Cookie")
      return
    }
    setLoading(true)
    setSelectedId("")
    const resp: PResp<{ content: FolderInfo[] }> = r.post(
      currentId() ? "/115/list_folders" : "/115/root_folders",
      {
        cookie,
        ...(currentId() ? { file_id: currentId() } : {}),
      },
    )
    const { code, message, data } = await resp
    setLoading(false)
    if (code !== 200) {
      notify.error(message)
      return
    }
    setFolders(data?.content || [])
  }

  // 每次弹窗打开时重置并加载根目录（用 wasOpened 防抖，避免
  // effect 读到 crumbs 又写入 [] 造成重渲染循环，导致"进入"子目录被反复重置）
  let wasOpened = false
  createEffect(() => {
    const op = props.opened()
    if (op && !wasOpened) {
      wasOpened = true
      setCrumbs([])
      setFolders([])
      void loadFolders()
    } else if (!op) {
      wasOpened = false
    }
  })

  const openFolder = (folder: FolderInfo) => {
    setCrumbs([...crumbs(), { file_id: folder.file_id, name: folder.name }])
    setFolders([])
    void loadFolders()
  }

  const goUp = () => {
    if (crumbs().length === 0) return
    setCrumbs(crumbs().slice(0, -1))
    setFolders([])
    void loadFolders()
  }

  const goCrumb = (i: number) => {
    setCrumbs(crumbs().slice(0, i + 1))
    setFolders([])
    void loadFolders()
  }

  // 选择当前所在目录（已进入的子目录）作为挂载根
  const pickCurrent = () => {
    const last = crumbs()[crumbs().length - 1]
    if (!last) {
      notify.warning("当前已在根目录，请选择要挂载的文件夹")
      return
    }
    props.onPick(last.file_id, last.name)
    props.onClose()
    notify.success(`已选择挂载文件夹: ${last.name}`)
  }

  const confirm = () => {
    const id = selectedId()
    if (!id) {
      notify.warning("请先选择要挂载的文件夹")
      return
    }
    const folder = folders().find((f) => f.file_id === id)
    props.onPick(id, folder?.name ?? id)
    props.onClose()
    notify.success(`已选择挂载文件夹: ${folder?.name ?? id}`)
  }

  return (
    <Modal blockScrollOnMount={false} opened={props.opened()} onClose={props.onClose}>
      <ModalOverlay />
      <ModalContent>
        <ModalHeader>选择要挂载的文件夹</ModalHeader>
        <ModalBody>
          <Show when={crumbs().length > 0}>
            <HStack spacing="$1" mb="$2" flexWrap="wrap">
              <Tag size="sm" colorScheme="neutral" cursor="pointer" onClick={goUp}>
                ← 上级
              </Tag>
              <For each={crumbs()}>
                {(crumb, i) => (
                  <Show when={i() < crumbs().length - 1}>
                    <Tag
                      size="sm"
                      colorScheme="info"
                      cursor="pointer"
                      onClick={() => goCrumb(i())}
                    >
                      {crumb.name}
                    </Tag>
                  </Show>
                )}
              </For>
              <Show when={crumbs().length > 0}>
                <Tag size="sm" colorScheme="accent">
                  {crumbs()[crumbs().length - 1].name}
                </Tag>
                <Button size="xs" colorScheme="success" onClick={pickCurrent}>
                  选择当前目录
                </Button>
              </Show>
            </HStack>
          </Show>
          <Show
            when={loading()}
            fallback={<Text>选择需要挂载的文件夹，可点击进入子文件夹，将自动填入根文件夹 ID</Text>}
          >
            <VStack spacing="$2">
              <Spinner />
              <Text size="sm">正在读取 115 网盘目录...</Text>
            </VStack>
          </Show>
          <Show when={!loading() && folders().length === 0}>
            <Text color="$danger9">未获取到文件夹列表，请确认 Cookie 是否有效</Text>
          </Show>
          <Show when={!loading() && folders().length > 0}>
            <RadioGroup value={selectedId()} onChange={setSelectedId}>
              <Stack direction="column" spacing="$1" maxHeight="$80" overflow="auto">
                <For each={folders()}>
                  {(folder) => (
                    <HStack
                      spacing="$2"
                      alignItems="center"
                      p="$1"
                      rounded="$sm"
                      cursor="pointer"
                      _hover={{ bgColor: "$neutral2" }}
                      onClick={() => setSelectedId(folder.file_id)}
                    >
                      <Radio value={folder.file_id}>{folder.name}</Radio>
                      <Box flex="1" />
                      <Button
                        size="xs"
                        colorScheme="info"
                        onClick={(e) => {
                          e.stopPropagation()
                          openFolder(folder)
                        }}
                      >
                        进入
                      </Button>
                    </HStack>
                  )}
                </For>
              </Stack>
            </RadioGroup>
          </Show>
        </ModalBody>
        <ModalFooter display="flex" gap="$2">
          <Button onClick={props.onClose} colorScheme="neutral">
            取消
          </Button>
          <Button onClick={confirm} disabled={!selectedId()}>
            确定
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

export type { FolderInfo, FolderCrumb }