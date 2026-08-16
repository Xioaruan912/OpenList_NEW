import {
  Alert,
  AlertIcon,
  Button,
  Heading,
  HStack,
  VStack,
} from "@hope-ui/solid"
import { createMemo, createSignal, For, Show } from "solid-js"
import { MaybeLoading, ModalInput } from "~/components"
import { useFetch, useRouter, useT } from "~/hooks"
import { handleResp, joinBase, notify, r } from "~/utils"
import { isDriverShown, parseDriversShow } from "~/utils/driverZh"
import {
  Addition,
  DriverConfig,
  DriverItem,
  Group,
  PResp,
  Storage,
  Type,
} from "~/types"
import { createStore, produce } from "solid-js/store"
import { Item } from "./Item"
import { QrcodeLogin115 } from "./QrcodeLogin115"
import { FolderPicker115 } from "./FolderPicker115"
import { ResponsiveGrid } from "../common/ResponsiveGrid"

interface DriverInfo {
  common: DriverItem[]
  additional: DriverItem[]
  config: DriverConfig
}

function GetDefaultValue(type: Type, value?: string) {
  switch (type) {
    case Type.Bool:
      if (value) {
        return value === "true"
      }
      return false
    case Type.Number:
      if (value) {
        return parseInt(value)
      }
      return 0
    case Type.Float:
      if (value) {
        return parseFloat(value)
      }
      return 0
    default:
      if (value) {
        return value
      }
      return ""
  }
}

type Drivers = Record<string, DriverInfo>

const AddOrEdit = () => {
  const t = useT()
  const { params, back, to } = useRouter()
  const { id } = params
  const [driversLoading, loadDrivers] = useFetch(
    (): PResp<Drivers> => r.get("/admin/driver/list"),
    true,
  )
  const [drivers, setDrivers] = createSignal<Drivers>({})
  const [panDriversShow, setPanDriversShow] = createSignal("")
  const loadPanDriversShow = async () => {
    const resp: PResp<{ key: string; value: string }[]> = r.get(
      `/admin/setting/list?group=${Group.GLOBAL}`,
    )
    const { code, data } = await resp
    if (code === 200) {
      setPanDriversShow(
        data.find((i) => i.key === "pan_drivers_show")?.value || "",
      )
    }
  }
  const shownDrivers = createMemo(() => {
    const all = Object.keys(drivers())
    return all.filter((d) => isDriverShown(d, parseDriversShow(panDriversShow())))
  })
  const initAdd = async () => {
    const resp = await loadDrivers()
    handleResp(resp, setDrivers)
    void loadPanDriversShow()
  }

  const [storageLoading, loadStorage] = useFetch(
    (): PResp<Storage> => r.get(`/admin/storage/get?id=${id}`),
    true,
  )
  const [driverLoading, loadDriver] = useFetch(
    (): PResp<DriverInfo> =>
      r.get(`/admin/driver/info?driver=${storage.driver}`),
    true,
  )
  const initEdit = async () => {
    const storageResp = await loadStorage()
    handleResp(storageResp, async (storageData) => {
      setStorage(storageData)
      setAddition(JSON.parse(storageData.addition))
      const driverResp = await loadDriver()
      handleResp(driverResp, (driverData) =>
        setDrivers({ [storage.driver]: driverData }),
      )
    })
  }
  if (id) {
    initEdit()
  } else {
    initAdd()
  }
  const [storage, setStorage] = createStore<Storage>({} as Storage)
  const [addition, setAddition] = createStore<Addition>({})
  const [okLoading, ok] = useFetch((): PResp<{ id: number }> => {
    setStorage("addition", JSON.stringify(addition))
    return r.post(`/admin/storage/${id ? "update" : "create"}`, storage)
  })
  const alert = createMemo(() => {
    const i = drivers()[storage.driver]?.config.alert
    console.log(i)
    if (i) {
      return i.split("|")[0]
    }
  })
  const [exportOpened, setExportOpened] = createSignal(false)
  const [importOpened, setImportOpened] = createSignal(false)
  const [pickerOpen, setPickerOpen] = createSignal(false)
  return (
    <MaybeLoading
      loading={id ? storageLoading() || driverLoading() : driversLoading()}
    >
      <Heading mb="$2">{t(`global.${id ? "edit" : "add"}`)}</Heading>
      <VStack mb="$2" spacing="$2">
        <Item
          name="driver"
          default=""
          readonly={id !== undefined}
          required
          searchable
          type={Type.Select}
          options={id ? storage.driver : shownDrivers().join(",")}
          value={storage.driver}
          full_name_path="storages.common.driver"
          options_prefix="drivers.drivers"
          driver="drivers"
          onChange={(value) => {
            for (const item of drivers()[value].common) {
              setStorage(
                item.name as keyof Storage,
                GetDefaultValue(item.type, item.default) as any,
              )
            }
            // clear addition first
            setAddition(
              produce((addition) => {
                for (const key in addition) {
                  delete addition[key]
                }
              }),
            )
            for (const item of drivers()[value].additional) {
              setAddition(
                item.name,
                GetDefaultValue(item.type, item.default) as any,
              )
            }
            setStorage("driver", value)
          }}
        />
        <Show when={alert()}>
          <Alert status={alert() as any} w="$full">
            <AlertIcon />
            {t(`drivers.config.${storage.driver}.alert`)}
          </Alert>
        </Show>
      </VStack>
      <ResponsiveGrid>
        <Show when={drivers()[storage.driver]}>
          <For each={drivers()[storage.driver].common}>
            {(item) => (
              <Item
                {...item}
                driver="common"
                value={(storage as any)[item.name]}
                onChange={(val: any) => {
                  setStorage(item.name as keyof Storage, val)
                }}
              />
            )}
          </For>
          <For each={drivers()[storage.driver].additional}>
            {(item) => (
              <Item
                {...item}
                driver={storage.driver}
                value={addition[item.name] as any}
                onChange={(val: any) => {
                  setAddition(item.name, val)
                }}
              />
            )}
          </For>
        </Show>
      </ResponsiveGrid>
      <Show when={storage.driver === "115 Cloud"}>
        <QrcodeLogin115
          setAddition={(name: string, value: any) => setAddition(name, value)}
        />
        <HStack mt="$2" spacing="$2">
          <Button
            colorScheme="primary"
            disabled={!addition.cookie}
            onClick={() => setPickerOpen(true)}
          >
            选择挂载文件夹
          </Button>
          <Show when={addition.root_folder_id}>
            <Heading size="sm" color="$neutral9">
              当前挂载文件夹：
              {(
                Array.isArray(addition.root_folder_id)
                  ? addition.root_folder_id.join(",")
                  : addition.root_folder_id
              ).split(",").length}{" "}
              个
              {(
                Array.isArray(addition.root_folder_id)
                  ? addition.root_folder_id.join(",")
                  : addition.root_folder_id
              ).split(",").length === 1
                ? `（ID ${addition.root_folder_id}）`
                : ""}
            </Heading>
          </Show>
        </HStack>
        <FolderPicker115
          opened={pickerOpen}
          cookie={() => addition.cookie || ""}
          current={addition.root_folder_id}
          onClose={() => setPickerOpen(false)}
          onPick={(ids) => setAddition("root_folder_id", ids.join(","))}
        />
      </Show>
      <HStack
        mt="$2"
        spacing="$2"
        gap="$2"
        w="$full"
        wrap={{
          "@initial": "wrap",
          "@md": "unset",
        }}
      >
        <Button
          loading={okLoading()}
          onClick={async () => {
            if (drivers()[storage.driver].config.need_ms) {
              notify.info(t("manage.add_storage-tips"))
              window.open(joinBase("/@manage/messenger"), "_blank")
            }
            const resp = await ok()
            // TODO maybe can use handleRrespWithNotifySuccess
            handleResp(
              resp,
              () => {
                notify.success(t("global.save_success"))
                back()
              },
              (msg, code) => {
                if (resp.data.id) {
                  to(`/@manage/storages/edit/${resp.data.id}`)
                }
              },
            )
          }}
        >
          {t(`global.${id ? "save" : "add"}`)}
        </Button>
        <Button
          colorScheme="accent"
          loading={okLoading()}
          onClick={async () => {
            setExportOpened(true)
          }}
        >
          {t("storages.common.export")}
        </Button>
        <Button
          colorScheme="primary"
          loading={okLoading()}
          onClick={async () => {
            setImportOpened(true)
          }}
        >
          {t("storages.common.import")}
        </Button>
      </HStack>
      <Show when={exportOpened()}>
        <ModalInput
          opened={exportOpened()}
          onClose={() => setExportOpened(false)}
          title={t("storages.common.export_title")}
          type="text"
          tips={t("storages.common.export_tips")}
          defaultValue={JSON.stringify(storage)}
          onSubmit={() => {
            setExportOpened(false)
          }}
        />
      </Show>
      <ModalInput
        opened={importOpened()}
        onClose={() => setImportOpened(false)}
        title={t("storages.common.import_title")}
        type="text"
        tips={t("storages.common.import_tips")}
        onSubmit={(text: string) => {
          try {
            const { id, disabled, modified, status, ...obj }: Storage =
              JSON.parse(text)
            setStorage(obj)
            setAddition(JSON.parse(obj.addition))
            setImportOpened(false)
            notify.success(t("storages.common.import_success"))
          } catch (e: any) {
            notify.error(`Invalid storage format: ${e.message}`)
          }
        }}
      />
    </MaybeLoading>
  )
}

export default AddOrEdit
