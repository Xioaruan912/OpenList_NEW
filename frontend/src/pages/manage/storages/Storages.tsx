import {
  Box,
  Button,
  Grid,
  HStack,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
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
  Table,
  Tbody,
  Tag,
  Text,
  Th,
  Thead,
  Tr,
  VStack,
  Switch as HopeSwitch,
} from "@hope-ui/solid"
import { createMemo, createSignal, For, Match, Show, Switch } from "solid-js"
import { useFetch, useManageTitle, useRouter, useT } from "~/hooks"
import { handleResp, notify, r } from "~/utils"
import { EmptyResp, Group, PageResp, Resp, Storage } from "~/types"
import { StorageGridItem, StorageListItem } from "./Storage"
import { createStorageSignal } from "@solid-primitives/storage"
import { driverZhName, isDriverShown, parseDriversShow } from "~/utils/driverZh"

type ThumbDirStat = { dir: string; count: number }
type ThumbStatus = {
  cached_files: number
  fail_markers: number
  cache_size: number
  prewarm_queued: number
  cached_by_dir?: ThumbDirStat[]
  fails_by_dir?: ThumbDirStat[]
}

const Storages = () => {
  const t = useT()
  useManageTitle("manage.sidemenu.storages")
  const { to } = useRouter()
  const [getStoragesLoading, getStorages] = useFetch(
    (): Promise<PageResp<Storage>> => r.get("/admin/storage/list"),
  )
  const [storages, setStorages] = createSignal<Storage[]>([])
  const refresh = async () => {
    const resp = await getStorages()
    handleResp(resp, (data) => setStorages(data.content))
  }
  const [drivers, setDrivers] = createSignal<string[]>([])
  const [selectedDrivers, setSelectedDrivers] = createSignal<string[]>([])
  const getDrivers = async () => {
    const resp: Resp<string[]> = await r.get("/admin/driver/names")
    handleResp(resp, (data) => setDrivers(data))
  }
  getDrivers()
  refresh()
  const [driversShowOpen, setDriversShowOpen] = createSignal(false)
  const [driversShowSel, setDriversShowSel] = createSignal<string[]>([])
  const [driversShowLoading, setDriversShowLoading] = createSignal(false)
  const loadDriversShow = async () => {
    const resp: Resp<{ key: string; value: string }[]> = r.get(
      `/admin/setting/list?group=${Group.GLOBAL}`,
    )
    const { code, data } = await resp
    if (code === 200) {
      const value =
        data.find((i) => i.key === "pan_drivers_show")?.value || ""
      const allow = parseDriversShow(value)
      setDriversShowSel(
        drivers().filter((d) => isDriverShown(d, allow)),
      )
    }
  }
  const saveDriversShow = async () => {
    setDriversShowLoading(true)
    const resp: Resp<{ key: string; value: string }[]> = r.get(
      `/admin/setting/list?group=${Group.GLOBAL}`,
    )
    const { code, data } = await resp
    if (code === 200) {
      const item = data.find((i) => i.key === "pan_drivers_show")
      const value = driversShowSel()
        .map((d) => driverZhName(d))
        .join(",")
      const saveResp: EmptyResp = await r.post("/admin/setting/save", [
        { ...item, value },
      ])
      handleResp(saveResp, () => {
        notify.success("驱动显示设置已保存")
        setDriversShowOpen(false)
      })
    }
    setDriversShowLoading(false)
  }
  const loadAll = async () => {
    const resp: EmptyResp = await r.post("/admin/storage/load_all")
    handleResp(resp, () => {
      notify.success(t("storages.other.start_load_success"))
    })
  }
  const shownStorages = createMemo(() => {
    return storages().filter((storage) => {
      if (selectedDrivers().length === 0) {
        return true
      }
      return selectedDrivers().includes(storage.driver)
    })
  })
  const [layout, setLayout] = createStorageSignal(
    "storages-layout",
    "grid" as "grid" | "table",
  )
  const [thumbOpen, setThumbOpen] = createSignal(false)
  const [thumbStatus, setThumbStatus] = createSignal<ThumbStatus>()
  const [thumbPage, setThumbPage] = createSignal(1)
  const loadThumbStatus = async () => {
    const resp: Resp<ThumbStatus> = await r.get("/admin/thumb/status")
    handleResp(resp, (data) => {
      setThumbStatus(data)
      setThumbPage(1)
    })
  }
  const retryThumbFails = async () => {
    const resp = await r.post("/admin/thumb/retry_fails", {})
    handleResp(resp, (data) => {
      const d = data as { retried?: number; cleared?: number }
      notify.success(`已重试：${d.retried || 0} 个入队，${d.cleared || 0} 个清除失败标记`)
      loadThumbStatus()
    })
  }
  const thumbDirs = () => {
    const out: (ThumbDirStat & { fail: boolean })[] = []
    ;(thumbStatus()?.fails_by_dir || []).forEach((e) =>
      out.push({ dir: e.dir, count: e.count, fail: true }),
    )
    const seen = new Set(out.map((e) => e.dir))
    ;(thumbStatus()?.cached_by_dir || []).forEach((e) => {
      if (!seen.has(e.dir)) out.push({ dir: e.dir, count: e.count, fail: false })
    })
    return out
  }
  const thumbPageCount = () =>
    Math.max(1, Math.ceil(thumbDirs().length / 5))
  const thumbPageDirs = () => thumbDirs().slice((thumbPage() - 1) * 5, thumbPage() * 5)
  return (
    <VStack spacing="$3" alignItems="start" w="$full">
      <HStack
        spacing="$2"
        gap="$2"
        w="$full"
        wrap={{
          "@initial": "wrap",
          "@md": "unset",
        }}
      >
        <Button
          colorScheme="accent"
          loading={getStoragesLoading()}
          onClick={refresh}
        >
          {t("global.refresh")}
        </Button>
        <Button
          onClick={() => {
            to("/@manage/storages/add")
          }}
        >
          {t("global.add")}
        </Button>
        <Button
          colorScheme="warning"
          loading={getStoragesLoading()}
          onClick={loadAll}
        >
          {t("storages.other.load_all")}
        </Button>
        <Button
          colorScheme="danger"
          variant="outline"
          onClick={() => {
            void loadDriversShow()
            setDriversShowOpen(true)
          }}
        >
          驱动显示设置
        </Button>
        <Button
          onClick={async () => {
            await loadThumbStatus()
            setThumbOpen(true)
          }}
        >
          缩略图状态
        </Button>
        <Show when={drivers().length > 0}>
          <Select
            multiple
            value={selectedDrivers()}
            onChange={setSelectedDrivers}
            // variant="outline"
          >
            <SelectTrigger>
              <SelectPlaceholder>
                {t("storages.other.filter_by_driver")}
              </SelectPlaceholder>
              <SelectValue />
              <SelectIcon />
            </SelectTrigger>
            <SelectContent>
              <SelectListbox>
                <For each={drivers()}>
                  {(item) => (
                    <SelectOption value={item}>
                      <SelectOptionText>
                        {t(`drivers.drivers.${item}`)}
                      </SelectOptionText>
                      <SelectOptionIndicator />
                    </SelectOption>
                  )}
                </For>
              </SelectListbox>
            </SelectContent>
          </Select>
        </Show>
        <HopeSwitch
          checked={layout() === "table"}
          onChange={(e: Event) => {
            setLayout(
              (e.currentTarget as HTMLInputElement).checked ? "table" : "grid",
            )
          }}
        >
          {t("storages.other.table_layout")}
        </HopeSwitch>
      </HStack>
      <Switch>
        <Match when={layout() === "grid"}>
          <Grid
            w="$full"
            gap="$2_5"
            templateColumns={{
              "@initial": "1fr",
              "@lg": "repeat(auto-fill, minmax(324px, 1fr))",
            }}
          >
            <For each={shownStorages()}>
              {(storage) => (
                <StorageGridItem storage={storage} refresh={refresh} />
              )}
            </For>
          </Grid>
        </Match>
        <Match when={layout() === "table"}>
          <Box w="$full" overflowX="auto">
            <Table highlightOnHover dense>
              <Thead>
                <Tr>
                  <For
                    each={[
                      "mount_path",
                      "driver",
                      "order",
                      "usage",
                      "status",
                      "remark",
                    ]}
                  >
                    {(title) => <Th>{t(`storages.common.${title}`)}</Th>}
                  </For>
                  <Th>{t("global.operations")}</Th>
                </Tr>
              </Thead>
              <Tbody>
                <For each={shownStorages()}>
                  {(storage) => (
                    <StorageListItem storage={storage} refresh={refresh} />
                  )}
                </For>
              </Tbody>
            </Table>
          </Box>
        </Match>
      </Switch>
      <Modal
        opened={thumbOpen()}
        onClose={() => setThumbOpen(false)}
        size={{ "@initial": "xs", "@md": "xl" }}
      >
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>缩略图状态</ModalHeader>
          <ModalBody>
            <Show
              when={thumbStatus()}
              fallback={<Box color="$neutral9">加载中...</Box>}
            >
              <Text color="$neutral10">
                缓存 {thumbStatus()!.cached_files} 个 / 队列{" "}
                {thumbStatus()!.prewarm_queued || 0} 个 / 失败{" "}
                {thumbStatus()!.fail_markers} 个 / 占用{" "}
                {(thumbStatus()!.cache_size / 1048576).toFixed(1)} MB
              </Text>
              <Text fontWeight="$medium" mt="$2">
                目录
              </Text>
              <VStack direction="column" spacing="$1" mt="$1" w="$full">
                <For each={thumbPageDirs()}>
                  {(e) => (
                    <Box
                      w="$full"
                      spacing="$1"
                      rounded="$md"
                      border="1px solid $neutral6"
                      p="$2"
                    >
                      <HStack spacing="$2" alignItems="center">
                        <Text fontWeight="$medium" css={{ "word-break": "break-all" }}>
                          {e.dir}
                        </Text>
                        <Tag colorScheme={e.fail ? "danger" : "info"}>
                          {e.count} 个
                        </Tag>
                        <Show when={e.fail}>
                          <Tag colorScheme="warning">有失败</Tag>
                        </Show>
                      </HStack>
                    </Box>
                  )}
                </For>
              </VStack>
              <Show when={thumbDirs().length > 5}>
                <HStack display="flex" gap="$2" alignItems="center" mt="$2">
                  <Button
                    size="sm"
                    disabled={thumbPage() <= 1}
                    onClick={() => setThumbPage(Math.max(1, thumbPage() - 1))}
                  >
                    上一页
                  </Button>
                  <Text fontSize="$sm">
                    {thumbPage()}/{thumbPageCount()}
                  </Text>
                  <Button
                    size="sm"
                    disabled={thumbPage() >= thumbPageCount()}
                    onClick={() => setThumbPage(thumbPage() + 1)}
                  >
                    下一页
                  </Button>
                </HStack>
              </Show>
            </Show>
          </ModalBody>
          <ModalFooter display="flex" gap="$2">
            <Button onClick={retryThumbFails}>重试失败</Button>
            <Button onClick={loadThumbStatus}>刷新</Button>
            <Button colorScheme="neutral" onClick={() => setThumbOpen(false)}>
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
      <Modal
        opened={driversShowOpen()}
        onClose={() => setDriversShowOpen(false)}
        size={{ "@initial": "xs", "@md": "md" }}
      >
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>驱动显示设置</ModalHeader>
          <ModalBody>
            <Text size="sm" color="$neutral9" mb="$2">
              选择"添加存储"下拉框中需要显示的驱动类型（不选即显示全部），保存后自动写入中文驱动名。
            </Text>
            <Show
              when={drivers().length > 0}
              fallback={<Box color="$neutral9">加载中...</Box>}
            >
              <Select
                multiple
                value={driversShowSel()}
                onChange={setDriversShowSel}
              >
                <SelectTrigger>
                  <SelectPlaceholder>选择要显示的驱动</SelectPlaceholder>
                  <SelectValue />
                  <SelectIcon />
                </SelectTrigger>
                <SelectContent>
                  <SelectListbox>
                    <For each={drivers()}>
                      {(item) => (
                        <SelectOption value={item}>
                          <SelectOptionText>{driverZhName(item)}</SelectOptionText>
                          <SelectOptionIndicator />
                        </SelectOption>
                      )}
                    </For>
                  </SelectListbox>
                </SelectContent>
              </Select>
            </Show>
          </ModalBody>
          <ModalFooter display="flex" gap="$2">
            <Button loading={driversShowLoading()} onClick={saveDriversShow}>
              保存
            </Button>
            <Button colorScheme="neutral" onClick={() => setDriversShowOpen(false)}>
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </VStack>
  )
}

export default Storages
