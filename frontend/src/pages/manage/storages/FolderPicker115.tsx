import {
  Box,
  Button,
  Checkbox,
  HStack,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
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

// FolderPicker115 可逐级浏览 115 网盘目录的可视化挂载文件夹选择器（支持多选）。
// 勾选多个文件夹，确认后一次性把全部 file_id 以逗号分隔写入 root_folder_id（驱动已支持多根）。
export const FolderPicker115 = (props: {
  opened: Accessor<boolean>
  cookie: Accessor<string>
  onClose: () => void
  onPick: (ids: string[], names: string[]) => void
  current?: string // 当前 root_folder_id（逗号分隔），打开时预勾选
}) => {
  const [loading, setLoading] = createSignal(false)
  const [folders, setFolders] = createSignal<FolderInfo[]>([])
  const [crumbs, setCrumbs] = createSignal<FolderCrumb[]>([])
  const [selected, setSelected] = createSignal<Record<string, boolean>>({})

  const currentId = () =>
    crumbs().length > 0 ? crumbs()[crumbs().length - 1].file_id : ""

  const loadFolders = async () => {
    const cookie = props.cookie()
    if (!cookie) {
      notify.warning("请先填写或获取 115 Cookie")
      return
    }
    setLoading(true)
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

  // 每次弹窗打开时重置并加载根目录；按当前 root_folder_id 预勾选
  let wasOpened = false
  createEffect(() => {
    const op = props.opened()
    if (op && !wasOpened) {
      wasOpened = true
      setCrumbs([])
      setFolders([])
      const pre: Record<string, boolean> = {}
      const cur =
        typeof props.current === "string"
          ? props.current
          : Array.isArray(props.current)
            ? props.current.join(",")
            : ""
      for (const id of cur.split(",")) {
        if (id.trim()) pre[id.trim()] = true
      }
      setSelected(pre)
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

  const toggle = (id: string) =>
    setSelected((prev) => {
      const z = { ...prev }
      z[id] = !z[id]
      return z
    })

  const selectedIds = () => Object.keys(selected()).filter((id) => selected()[id])

  const finish = () => {
    const ids = selectedIds()
    if (!ids.length) {
      notify.warning("请先勾选要挂载的文件夹")
      return
    }
    const names = ids.map(
      (id) => folders().find((f) => f.file_id === id)?.name || id,
    )
    props.onPick(ids, names)
    props.onClose()
    notify.success(`已选择挂载 ${ids.length} 个文件夹`)
  }

  // 选择当前所在目录（已进入的子目录）并追加到选中集合
  const pickCurrent = () => {
    const last = crumbs()[crumbs().length - 1]
    if (!last) {
      notify.warning("当前已在根目录，请直接勾选要挂载的文件夹")
      return
    }
    setSelected((prev) => ({ ...prev, [last.file_id]: true }))
    finish()
  }

  return (
    <Modal blockScrollOnMount={false} opened={props.opened()} onClose={props.onClose}>
      <ModalOverlay />
      <ModalContent>
        <ModalHeader>选择要挂载的文件夹（可多选）</ModalHeader>
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
            fallback={
              <Text>勾选需要挂载的文件夹（可多个），可进入子文件夹，确认后写入根文件夹 ID（逗号分隔）</Text>
            }
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
                    onClick={() => toggle(folder.file_id)}
                  >
                    <Checkbox
                      checked={!!selected()[folder.file_id]}
                      onChange={(e: any) => {
                        e.stopPropagation()
                        toggle(folder.file_id)
                      }}
                      on:click={(e: MouseEvent) => e.stopPropagation()}
                    >
                      {folder.name}
                    </Checkbox>
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
          </Show>
        </ModalBody>
        <ModalFooter display="flex" gap="$2">
          <Button onClick={props.onClose} colorScheme="neutral">
            取消
          </Button>
          <Button onClick={finish} disabled={selectedIds().length === 0}>
            确定（已选 {selectedIds().length} 个）
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

export type { FolderInfo, FolderCrumb }
