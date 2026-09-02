import { Box, Button, HStack, Tag, Text, useColorModeValue } from "@hope-ui/solid"
import { For, Show } from "solid-js"

type Props = {
  selected: string
  files: string[]
  thumbCount: number
  excluded: string[]
  hasThumb: Record<string, boolean>
  listed: boolean
  checked: Record<string, boolean>
  failed: Record<string, string>
  busy: string
  candidateLoadingPath: string
  onGenerate: () => void
  onUpload: () => void
  onRetryFailed: () => void
  onDeleteChecked: () => void
  onExcludeUnchecked: () => void
  onExcludeAll: () => void
  onRestoreExcluded: () => void
  onRestoreAll: () => void
  onToggleFile: (path: string) => void
  onView: (path: string) => void
  onCandidates: (path: string) => void
  onFailLog: (path: string) => void
}

export const DirectoryDetail = (props: Props) => {
  const hoverBg = useColorModeValue("$neutral2", "$neutral3")

  return (
    <Box w={{ "@initial": "$full", "@md": "50%" }} rounded="$lg" border="1px solid $neutral6" p="$2">
      <Text fontWeight="$medium" css={{ "word-break": "break-all" }}>
        {props.selected || "未选择目录"}
      </Text>
      <HStack spacing="$1" mt="$2" wrap="wrap">
        <Tag colorScheme="neutral">共有 {props.files.length} 个媒体</Tag>
        <Tag colorScheme="info">已有缩略图 {props.thumbCount} 个</Tag>
        <Show when={!props.listed}>
          <Tag colorScheme="warning">列表受限（115 风控），仅展示有缩略图的文件</Tag>
        </Show>
        <Tag colorScheme={props.excluded.length ? "warning" : "neutral"}>已排除 {props.excluded.length} 个</Tag>
        <Button
          size="xs"
          colorScheme="accent"
          disabled={!props.selected || props.busy === "gen-" + props.selected}
          onClick={props.onGenerate}
        >
          生成
        </Button>
        <Button
          size="xs"
          colorScheme="info"
          disabled={!props.selected || props.busy === "up-" + props.selected}
          onClick={props.onUpload}
        >
          上传
        </Button>
        <Button size="xs" colorScheme="warning" disabled={!props.selected} onClick={props.onRetryFailed}>
          重试失败
        </Button>
        <Button
          size="xs"
          colorScheme="danger"
          disabled={!props.selected || props.busy === "delpaths-" + props.selected}
          onClick={props.onDeleteChecked}
        >
          删除
        </Button>
      </HStack>
      <HStack spacing="$2" alignItems="center" mt="$2" wrap="wrap">
        <Button size="xs" colorScheme="warning" disabled={!props.selected} onClick={props.onExcludeUnchecked}>
          排除未勾选
        </Button>
        <Button size="xs" colorScheme="danger" disabled={!props.selected} onClick={props.onExcludeAll}>
          全排除
        </Button>
        <Button size="xs" disabled={!props.selected || !props.excluded.length} onClick={props.onRestoreExcluded}>
          恢复已排除
        </Button>
        <Button size="xs" colorScheme="success" disabled={!props.selected} onClick={props.onRestoreAll}>
          恢复全部纳入
        </Button>
        <Text fontSize="$xs" color="$neutral9">
          勾选 = 纳入操作（生成/删除），取消勾选 = 排除
        </Text>
      </HStack>
      <Box mt="$2" maxH="420px" overflowY="auto" rounded="$md" border="1px solid $neutral6" p="$1">
        <For each={props.files}>
          {(path) => (
            <HStack
              direction="row"
              spacing="$2"
              alignItems="center"
              wrap="wrap"
              p="$2"
              rounded="$sm"
              _hover={{ bgColor: hoverBg() }}
            >
              <Button
                size="xs"
                variant="subtle"
                colorScheme={props.checked[path] ? "success" : "neutral"}
                onClick={() => props.onToggleFile(path)}
              >
                {props.checked[path] ? "✓" : "○"}
              </Button>
              <Show
                when={props.hasThumb[path]}
                fallback={
                  <Box
                    css={{
                      flex: "1 1 auto",
                      "word-break": "break-all",
                      "font-size": "$sm",
                      opacity: props.checked[path] ? "1" : "0.5",
                      cursor: "pointer",
                    }}
                    title="无缩略图，点击选择画面"
                    onClick={() => props.onCandidates(path)}
                    _hover={{ color: "$info9" }}
                  >
                    {path.replace(props.selected, "").replace(/^\//, "")}
                  </Box>
                }
              >
                <Box
                  css={{
                    flex: "1 1 auto",
                    "word-break": "break-all",
                    "font-size": "$sm",
                    opacity: props.checked[path] ? "1" : "0.5",
                    cursor: "pointer",
                  }}
                  title="点击查看缩略图"
                  onClick={() => props.onView(path)}
                  _hover={{ color: "$info9" }}
                >
                  {path.replace(props.selected, "").replace(/^\//, "")}
                </Box>
              </Show>
              <Show when={!props.hasThumb[path]}>
                <Tag colorScheme="neutral" size="sm">
                  无缩略图
                </Tag>
                <Button
                  size="xs"
                  variant="ghost"
                  colorScheme="info"
                  loading={props.candidateLoadingPath === path}
                  onClick={() => props.onCandidates(path)}
                >
                  选择画面
                </Button>
              </Show>
              <Show when={props.failed[path]}>
                <Tag colorScheme="danger" size="sm" title={props.failed[path] || "生成失败"}>
                  失败
                </Tag>
                <Button size="xs" variant="ghost" colorScheme="danger" onClick={() => props.onFailLog(path)}>
                  日志
                </Button>
              </Show>
              <Show when={!props.checked[path]}>
                <Tag colorScheme="warning" size="sm">
                  已排除
                </Tag>
              </Show>
            </HStack>
          )}
        </For>
      </Box>
    </Box>
  )
}
