import { Box, Button, HStack, Tag, Text, VStack, useColorModeValue } from "@hope-ui/solid"
import { For, Show } from "solid-js"
import type { TreeNode } from "../types"

type Props = {
  tree: TreeNode[]
  selected: string
  expanded: Set<string>
  loading: boolean
  scanStatus: string
  busy: string
  onSelect: (path: string) => void
  onToggle: (path: string) => void
  onGenerate: (path: string) => void
  onUpload: (path: string) => void
  onDelete: (path: string) => void
}

const cachedColor = (node: TreeNode) => {
  const total = node.cached || 0
  if (total === 0) return "neutral"
  const local = node.local || 0
  const cloud = node.cloud || 0
  if (cloud > 0 && local === 0) return "success"
  if (local > 0 && cloud === 0) return "warning"
  return "neutral"
}

export const ThumbTreeView = (props: Props) => {
  const hoverBg = useColorModeValue("$neutral2", "$neutral3")
  const selectedBg = useColorModeValue("$info2", "$info3")
  const normalBg = useColorModeValue("$neutral1", "$neutral2")

  const renderNode = (node: TreeNode, depth: number) => (
    <>
      <HStack
        spacing="$1"
        alignItems="center"
        w="$full"
        p="$2"
        rounded="$md"
        wrap="wrap"
        _hover={{ bgColor: hoverBg() }}
        background={props.selected === node.path ? selectedBg() : normalBg()}
        style={{ "padding-left": `${10 + depth * 10}px`, cursor: "pointer" }}
        onClick={() => props.onSelect(node.path)}
      >
        <Show when={(node.children || []).length > 0} fallback={<Box w="$7" />}>
          <Button
            size="xs"
            variant="subtle"
            onClick={(event) => {
              event.stopPropagation()
              props.onToggle(node.path)
            }}
          >
            {props.expanded.has(node.path) ? "▾" : "▸"}
          </Button>
        </Show>
        <Box mr="$1">📁</Box>
        <Box
          css={{
            flex: "1 1 auto",
            "font-size": "$sm",
            "white-space": "nowrap",
            overflow: "hidden",
            "text-overflow": "ellipsis",
          }}
          title={node.name}
        >
          {node.name}
        </Box>
        <Tag colorScheme={cachedColor(node)}>{node.cached}</Tag>
        <Show when={(node.videos || 0) > node.cached}>
          <Tag colorScheme="warning">缺 {node.videos! - node.cached}</Tag>
        </Show>
        <Button
          size="xs"
          disabled={props.busy === node.path}
          onClick={(event) => {
            event.stopPropagation()
            props.onGenerate(node.path)
          }}
        >
          生成
        </Button>
        <Button
          size="xs"
          colorScheme="info"
          disabled={props.busy === "up-" + node.path}
          onClick={(event) => {
            event.stopPropagation()
            props.onUpload(node.path)
          }}
        >
          上传
        </Button>
        <Button
          size="xs"
          colorScheme="danger"
          disabled={props.busy === "del-" + node.path}
          onClick={(event) => {
            event.stopPropagation()
            props.onDelete(node.path)
          }}
        >
          删除
        </Button>
      </HStack>
      <Show when={props.expanded.has(node.path) && (node.children || []).length > 0}>
        <For each={node.children}>{(child) => renderNode(child, depth + 1)}</For>
      </Show>
    </>
  )

  return (
    <Box w={{ "@initial": "$full", "@md": "50%" }} rounded="$lg" border="1px solid $neutral6" p="$1">
      <HStack p="$2" spacing="$2" alignItems="center" wrap="wrap">
        <Text fontWeight="$medium">目录</Text>
        <Show when={props.scanStatus === "refreshing"}>
          <Tag colorScheme="info" size="sm">
            后台校准中
          </Tag>
        </Show>
        <Show when={props.scanStatus === "partial"}>
          <Tag colorScheme="warning" size="sm">
            部分目录暂不可达
          </Tag>
        </Show>
      </HStack>
      <Show when={props.loading}>
        <Text p="$2" fontSize="$sm" color="$neutral9">
          加载中...
        </Text>
      </Show>
      <VStack direction="column" w="$full">
        <For each={props.tree}>{(node) => renderNode(node, 0)}</For>
      </VStack>
    </Box>
  )
}
