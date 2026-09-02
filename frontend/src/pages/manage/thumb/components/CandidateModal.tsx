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
  Progress,
  ProgressIndicator,
  Tag,
  Text,
  useColorModeValue,
} from "@hope-ui/solid"
import { For, Show } from "solid-js"
import type { ThumbCandidate } from "../types"

type Props = {
  opened: boolean
  path: string
  viewUrl: string
  viewLoading: boolean
  loading: boolean
  jobId: string
  progress: number
  candidates: ThumbCandidate[]
  sheet: string
  recommendedIndex: number
  cached: boolean
  riskBlocked: boolean
  truncated: boolean
  applying: boolean
  onClose: () => void
  onGenerate: (refresh: boolean) => void
  onCancel: () => void
  onApply: (png: string, message?: string) => void
}

export const CandidateModal = (props: Props) => {
  const panelBg = useColorModeValue("$neutral1", "$neutral2")

  return (
    <Modal opened={props.opened} onClose={props.onClose} size={{ "@initial": "sm", "@md": "md" }}>
      <ModalOverlay />
      <ModalContent>
        <ModalHeader css={{ "word-break": "break-all", "font-size": "$sm" }}>
          {props.path.split("/").pop()}
        </ModalHeader>
        <ModalBody>
          <Show when={props.viewLoading} fallback={<Box />}>
            <Text p="$4" fontSize="$sm" color="$neutral9">
              加载中…
            </Text>
          </Show>
          <Show when={props.viewUrl}>
            <Box
              rounded="$md"
              border="1px solid $neutral6"
              overflow="hidden"
              background="#000"
              css={{ display: "flex", justifyContent: "center", alignItems: "center", maxH: "45vh" }}
            >
              <img
                src={props.viewUrl}
                alt={props.path}
                css={{ maxWidth: "100%", maxHeight: "45vh", objectFit: "contain" }}
              />
            </Box>
          </Show>
          <HStack mt="$2" spacing="$2" wrap="wrap">
            <Button
              size="xs"
              colorScheme="info"
              loading={props.loading}
              disabled={!props.path || props.loading}
              onClick={() => props.onGenerate(props.candidates.length > 0 || props.cached)}
            >
              {props.candidates.length || props.cached ? "重新生成候选" : "生成候选九宫格"}
            </Button>
            <Show when={props.cached}>
              <Tag colorScheme="neutral" size="sm">
                已使用缓存
              </Tag>
            </Show>
            <Show when={props.loading && props.jobId}>
              <Button size="xs" colorScheme="danger" variant="outline" onClick={props.onCancel}>
                取消任务
              </Button>
              <Tag colorScheme="info" size="sm">
                可关闭窗口，后台继续
              </Tag>
            </Show>
            <Text fontSize="$xs" color="$neutral9">
              115 安全模式：候选帧单路生成，检测到风控会立即停止
            </Text>
          </HStack>
          <Show when={props.loading && props.jobId}>
            <Box mt="$2">
              <HStack spacing="$2" justifyContent="space-between">
                <Text fontSize="$xs" color="$neutral9">
                  后台取帧 {Math.min(9, Math.ceil((props.progress / 100) * 9))} / 9
                </Text>
                <Text fontSize="$xs" color="$neutral9">
                  {Math.round(props.progress)}%
                </Text>
              </HStack>
              <Progress value={props.progress} max={100} size="sm">
                <ProgressIndicator color="$info6" />
              </Progress>
            </Box>
          </Show>
          <Show when={props.sheet}>
            <Box mt="$3" rounded="$md" border="1px solid $neutral6" p="$2" background={panelBg()}>
              <Text fontSize="$sm" fontWeight="$medium">
                候选九宫格
              </Text>
              <img
                src={`data:image/png;base64,${props.sheet}`}
                alt="候选九宫格"
                css={{
                  width: "100%",
                  maxHeight: "30vh",
                  objectFit: "contain",
                  background: "#101010",
                  display: "block",
                  marginTop: "6px",
                }}
              />
              <HStack mt="$2" spacing="$2" wrap="wrap">
                <Button
                  size="xs"
                  colorScheme="success"
                  loading={props.applying}
                  disabled={props.loading}
                  onClick={() => props.onApply(props.sheet, "已应用九宫格缩略图")}
                >
                  保存九宫格
                </Button>
                <Show
                  when={
                    props.recommendedIndex > 0 &&
                    props.candidates.some((candidate) => candidate.index === props.recommendedIndex)
                  }
                >
                  <Button
                    size="xs"
                    colorScheme="accent"
                    loading={props.applying}
                    disabled={props.loading}
                    onClick={() => {
                      const recommended = props.candidates.find(
                        (candidate) => candidate.index === props.recommendedIndex,
                      )
                      if (recommended) props.onApply(recommended.png, "已应用推荐缩略图")
                    }}
                  >
                    采用推荐画面
                  </Button>
                </Show>
                <Text fontSize="$xs" color="$neutral9">
                  已取得 {props.candidates.length}/9 帧
                </Text>
              </HStack>
              <Show when={props.riskBlocked || props.truncated}>
                <Text mt="$1" fontSize="$xs" color="$warning9">
                  {props.riskBlocked
                    ? "检测到 115 风控迹象，已停止后续取帧；请勿立即重复生成。"
                    : "候选生成未完整结束，已显示当前已取得的画面。"}
                </Text>
              </Show>
            </Box>
          </Show>
          <Show when={props.candidates.length}>
            <Grid mt="$3" w="$full" gap="$2" templateColumns={{ "@initial": "1fr", "@sm": "repeat(3, 1fr)" }}>
              <For each={props.candidates}>
                {(candidate) => (
                  <Box rounded="$md" border="1px solid $neutral6" p="$1" background={panelBg()}>
                    <img
                      src={`data:image/png;base64,${candidate.png}`}
                      alt={`候选${candidate.index}`}
                      css={{ width: "100%", borderRadius: "6px", display: "block" }}
                    />
                    <HStack mt="$1" spacing="$1" justifyContent="space-between" alignItems="center">
                      <HStack spacing="$1" alignItems="center">
                        <Text fontSize="$xs" color="$neutral9">
                          {Number(candidate.at) >= 60
                            ? `${Math.floor(Number(candidate.at) / 60)}m${Math.round(Number(candidate.at) % 60)}s`
                            : `${candidate.at}s`}
                        </Text>
                        <Show when={candidate.index === props.recommendedIndex}>
                          <Tag colorScheme="success" size="sm">
                            推荐
                          </Tag>
                        </Show>
                      </HStack>
                      <Button
                        size="xs"
                        colorScheme="success"
                        loading={props.applying}
                        disabled={props.loading}
                        onClick={() => props.onApply(candidate.png)}
                      >
                        保留此图
                      </Button>
                    </HStack>
                  </Box>
                )}
              </For>
            </Grid>
          </Show>
        </ModalBody>
        <ModalFooter display="flex" gap="$2" justifyContent="flex-end">
          <Button colorScheme="neutral" onClick={props.onClose}>
            {props.loading && props.jobId ? "放到后台" : "关闭"}
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}
