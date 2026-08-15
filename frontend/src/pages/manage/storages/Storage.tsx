import {
  Badge,
  Box,
  Button,
  HStack,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Td,
  Tag,
  Text,
  Tr,
  useColorModeValue,
  VStack,
  Progress,
  ProgressIndicator,
  ProgressLabel,
} from "@hope-ui/solid"
import { createSignal, Show } from "solid-js"
import { useFetch, useRouter, useT } from "~/hooks"
import { getMainColor } from "~/store"
import { MountDetails, PEmptyResp, Resp, Storage } from "~/types"
import {
  handleResp,
  handleRespWithNotifySuccess,
  notify,
  r,
  showDiskUsage,
  usedPercentage,
  toReadableUsage,
  nearlyFull,
} from "~/utils"
import { DeletePopover } from "../common/DeletePopover"

interface StorageProps {
  storage: Storage
  refresh: () => void
}

function StorageOp(props: StorageProps) {
  const t = useT()
  const { to } = useRouter()
  const [deleteLoading, deleteStorage] = useFetch((): PEmptyResp =>
    r.post(`/admin/storage/delete?id=${props.storage.id}`),
  )
  const [enableOrDisableLoading, enableOrDisable] = useFetch((): PEmptyResp =>
    r.post(
      `/admin/storage/${props.storage.disabled ? "enable" : "disable"}?id=${
        props.storage.id
      }`,
    ),
  )
  const is115 =
    props.storage.driver === "115 Cloud" || props.storage.driver === "115 Share"
  const [fkOpen, setFkOpen] = createSignal(false)
  const [fkLoading, setFkLoading] = createSignal(false)
  const [fkResult, setFkResult] = createSignal<{ blocked: boolean; msg: string }>()
  const checkBlocked = async () => {
    setFkLoading(true)
    const resp: Resp<{ blocked: boolean; msg: string }> = await r.post(
      "/admin/storage/check_blocked",
      { id: props.storage.id },
    )
    setFkLoading(false)
    if (resp.code === 200) {
      setFkResult({ blocked: !!resp.data.blocked, msg: resp.data.msg || "" })
    } else {
      setFkResult({ blocked: false, msg: resp.message })
    }
    setFkOpen(true)
  }
  return (
    <>
      <Show when={is115}>
        <Button
          colorScheme="warning"
          loading={fkLoading()}
          onClick={checkBlocked}
        >
          风控
        </Button>
      </Show>
      <Button
        onClick={() => {
          to(`/@manage/storages/edit/${props.storage.id}`)
        }}
      >
        {t("global.edit")}
      </Button>
      <Button
        loading={enableOrDisableLoading()}
        colorScheme={props.storage.disabled ? "success" : "warning"}
        onClick={async () => {
          const resp = await enableOrDisable()
          handleRespWithNotifySuccess(resp, () => {
            props.refresh()
          })
        }}
      >
        {t(`global.${props.storage.disabled ? "enable" : "disable"}`)}
      </Button>
      <DeletePopover
        name={props.storage.mount_path}
        loading={deleteLoading()}
        onClick={async () => {
          const resp = await deleteStorage()
          handleResp(resp, () => {
            notify.success(t("global.delete_success"))
            props.refresh()
          })
        }}
      />
      <Modal opened={fkOpen()} onClose={() => setFkOpen(false)} size="xs">
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>115 风控检查</ModalHeader>
          <ModalBody>
            <HStack spacing="$2" alignItems="center">
              <Tag colorScheme={fkResult()?.blocked ? "danger" : "success"}>
                {fkResult()?.blocked ? "风控中" : "正常"}
              </Tag>
              <Text fontWeight="$medium" css={{ "word-break": "break-all" }}>
                {props.storage.mount_path}
              </Text>
            </HStack>
            <Show when={fkResult()?.msg}>
              <Text size="sm" color="$neutral9" mt="$2">
                {fkResult()?.msg}
              </Text>
            </Show>
          </ModalBody>
          <ModalFooter display="flex" gap="$2">
            <Button colorScheme="neutral" onClick={() => setFkOpen(false)}>
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </>
  )
}

function StorageUsage(props: { details: MountDetails | undefined }) {
  return (
    <Show when={props.details}>
      <Progress
        class="disk-usage-percentage"
        trackColor="$info3"
        rounded="$full"
        size="md"
        value={usedPercentage(props.details!)}
      >
        <ProgressIndicator
          color={nearlyFull(props.details!) ? "$danger6" : "$info6"}
          rounded="$md"
        />
        <ProgressLabel class="disk-usage-text">
          {toReadableUsage(props.details!)}
        </ProgressLabel>
      </Progress>
    </Show>
  )
}

export function StorageGridItem(props: StorageProps) {
  const t = useT()
  return (
    <VStack
      w="$full"
      spacing="$2"
      rounded="$lg"
      border="1px solid $neutral7"
      background={useColorModeValue("$neutral2", "$neutral3")()}
      // alignItems="start"
      p="$3"
      _hover={{
        border: `1px solid ${getMainColor()}`,
      }}
    >
      <HStack spacing="$2">
        <Text
          fontWeight="$medium"
          css={{
            wordBreak: "break-all",
          }}
        >
          {props.storage.mount_path}
        </Text>
        <Badge colorScheme="info">
          {t(`drivers.drivers.${props.storage.driver}`)}
        </Badge>
        <Show when={props.storage.mount_details}>
          <Badge
            colorScheme={
              nearlyFull(props.storage.mount_details!) ? "danger" : "success"
            }
          >
            {toReadableUsage(props.storage.mount_details!)}
          </Badge>
        </Show>
      </HStack>
      <HStack>
        <Text>{t("storages.common.status")}:&nbsp;</Text>
        <Box
          css={{ wordBreak: "break-all" }}
          overflowX="auto"
          innerHTML={t(
            `storages.table_fields.status.${props.storage.status}`,
            undefined,
            props.storage.status,
          )}
        />
      </HStack>
      <Text css={{ wordBreak: "break-all" }}>{props.storage.remark}</Text>
      <HStack spacing="$2">
        <StorageOp {...props} />
      </HStack>
    </VStack>
  )
}

export function StorageListItem(props: StorageProps) {
  const t = useT()
  return (
    <Tr>
      <Td>{props.storage.mount_path}</Td>
      <Td>{t(`drivers.drivers.${props.storage.driver}`)}</Td>
      <Td>{props.storage.order}</Td>
      <Td>
        <StorageUsage details={props.storage.mount_details} />
      </Td>
      <Td>
        {t(
          `storages.table_fields.status.${props.storage.status}`,
          undefined,
          props.storage.status,
        )}
      </Td>
      <Td>{props.storage.remark}</Td>
      <Td>
        <HStack spacing="$2">
          <StorageOp {...props} />
        </HStack>
      </Td>
    </Tr>
  )
}
