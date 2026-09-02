import { Box, Button, HStack, Progress, ProgressIndicator, Tag, Text } from "@hope-ui/solid"
import { For, Show } from "solid-js"
import type { ThumbStatus, UploadStatus } from "../types"

type GenerationQueueProps = {
  status?: ThumbStatus
  queued: number
  active: number
  totalQueued: number
  baseCached: number | null
  busy: string
  onOpenFails: () => void
  onPause: () => void
  onResume: () => void
  onClear: () => void
}

export const GenerationQueuePanel = (props: GenerationQueueProps) => (
  <Box mt="$2" rounded="$lg" border="1px solid $neutral6" p="$2" w="$full">
    <HStack spacing="$2" alignItems="center" wrap="wrap">
      <Tag
        colorScheme={
          props.status?.blocked || props.status?.queue_paused
            ? "danger"
            : props.active > 0
              ? "success"
              : props.queued > 0
                ? "warning"
                : "neutral"
        }
      >
        {props.status?.blocked
          ? "部分 115 存储风控，相关生成暂停"
          : props.status?.queue_paused
            ? "队列已暂停"
            : props.active > 0
              ? "正在生成中"
              : props.queued > 0
                ? "已入队，等待生成"
                : "空闲"}
      </Tag>
      <Text fontSize="$sm" color="$neutral9">
        {props.active > 0 ? `正在生成 ${props.active} 个 · ` : ""}队列剩余 {props.queued} 个 · 本次已生成{" "}
        {Math.max(0, (props.status?.cached_files || 0) - (props.baseCached ?? 0))} 个 · 失败{" "}
        {props.status?.fail_markers || 0} 个
      </Text>
      <Show when={(props.status?.fail_markers || 0) > 0}>
        <Button size="xs" variant="outline" colorScheme="danger" onClick={props.onOpenFails}>
          查看失败日志
        </Button>
      </Show>
      <Button
        size="xs"
        colorScheme={props.status?.queue_paused ? "success" : "warning"}
        disabled={props.busy === "queue-pause" || props.busy === "queue-resume"}
        onClick={() => (props.status?.queue_paused ? props.onResume() : props.onPause())}
      >
        {props.status?.queue_paused ? "恢复队列" : "暂停队列"}
      </Button>
      <Button size="xs" colorScheme="danger" disabled={props.busy === "queue-clear" || !props.queued} onClick={props.onClear}>
        删除队列
      </Button>
    </HStack>
    <Show when={(props.status?.active_tasks || []).length > 0}>
      <Box mt="$2" rounded="$md" border="1px solid $neutral6" p="$1">
        <For each={props.status!.active_tasks}>
          {(task) => (
            <Text fontSize="$xs" color="$neutral9" css={{ "word-break": "break-all" }}>
              ▶ {task.path.split("/").pop()}
            </Text>
          )}
        </For>
      </Box>
    </Show>
    <Progress
      mt="$2"
      value={
        props.totalQueued > 0
          ? Math.max(0, Math.min(100, Math.round(((props.totalQueued - props.queued - props.active) / props.totalQueued) * 100)))
          : 0
      }
      max={100}
      indeterminate={props.active > 0 && props.queued + props.active >= props.totalQueued}
      size="sm"
    >
      <ProgressIndicator color="$info6" />
    </Progress>
  </Box>
)

type UploadQueueProps = {
  status?: ThumbStatus
  upload: UploadStatus
  busy: string
  onOpenFails: () => void
  onRetry: () => void
  onPause: () => void
  onResume: () => void
  onClear: () => void
}

export const UploadQueuePanel = (props: UploadQueueProps) => (
  <Box mt="$2" rounded="$lg" border="1px solid $neutral6" p="$2" w="$full">
    <HStack spacing="$2" alignItems="center" wrap="wrap">
      <Tag
        colorScheme={
          props.upload.paused
            ? "warning"
            : props.status?.blocked
              ? "warning"
              : props.upload.active
                ? "success"
                : props.upload.remaining > 0
                  ? "warning"
                  : "neutral"
        }
      >
        {props.upload.paused
          ? "上传已暂停"
          : props.status?.blocked
            ? "部分 115 存储风控，相关上传暂停"
            : props.upload.active
              ? "正在上传"
              : props.upload.remaining > 0
                ? "已入队，等待上传"
                : props.upload.total > 0
                  ? "上传完成"
                  : "空闲"}
      </Tag>
      <Text fontSize="$sm" color="$neutral9">
        剩余 {props.upload.remaining} 个 · 成功 {props.upload.done} · 已存在(网盘已有) {props.upload.exists} · 失败{" "}
        {props.upload.failed}
      </Text>
      <Show when={props.upload.fails > 0}>
        <Button size="xs" variant="outline" colorScheme="danger" onClick={props.onOpenFails}>
          查看失败日志
        </Button>
        <Button size="xs" colorScheme="warning" loading={props.busy === "upload-retry"} onClick={props.onRetry}>
          重试上传失败
        </Button>
      </Show>
      <Button
        size="xs"
        variant="outline"
        colorScheme={props.upload.paused ? "success" : "warning"}
        disabled={props.busy === "upload-pause" || props.busy === "upload-resume"}
        onClick={() => (props.upload.paused ? props.onResume() : props.onPause())}
      >
        {props.upload.paused ? "恢复上传" : "暂停上传"}
      </Button>
      <Button
        size="xs"
        variant="outline"
        colorScheme="danger"
        disabled={props.busy === "upload-clear" || props.upload.total === 0}
        onClick={props.onClear}
      >
        删除上传队列
      </Button>
    </HStack>
    <Progress
      mt="$2"
      value={
        props.upload.total > 0
          ? Math.max(0, Math.min(100, Math.round(((props.upload.done + props.upload.exists) / props.upload.total) * 100)))
          : 0
      }
      max={100}
      indeterminate={props.upload.active && props.upload.done + props.upload.exists + props.upload.failed === 0}
      size="sm"
    >
      <ProgressIndicator color="$success6" />
    </Progress>
  </Box>
)
