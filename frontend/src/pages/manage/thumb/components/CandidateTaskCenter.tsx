import { Box, Button, HStack, Progress, ProgressIndicator, Tag, Text, VStack, useColorModeValue } from "@hope-ui/solid"
import { For, Show } from "solid-js"
import type { CandidateJobState, CandidateJobSummary } from "../types"

type Props = {
  jobs: CandidateJobSummary[]
  onOpen: (job: CandidateJobSummary) => void
  onCancel: (job: CandidateJobSummary) => void
}

const stateText = (state: CandidateJobState) => {
  switch (state) {
    case "queued":
      return "等待中"
    case "running":
      return "正在生成"
    case "succeeded":
      return "已完成"
    case "failed":
      return "失败"
    case "canceled":
      return "已取消"
  }
}

const stateColor = (state: CandidateJobState) => {
  switch (state) {
    case "running":
    case "succeeded":
      return "success"
    case "queued":
      return "warning"
    case "failed":
      return "danger"
    default:
      return "neutral"
  }
}

export const CandidateTaskCenter = (props: Props) => (
  <Show when={props.jobs.length > 0}>
    <Box mt="$2" rounded="$lg" border="1px solid $neutral6" p="$2" w="$full">
      <HStack spacing="$2" alignItems="center" wrap="wrap" mb="$2">
        <Text fontWeight="$semibold">3×3 后台任务</Text>
        <Tag colorScheme="info">{props.jobs.filter((job) => job.state === "queued" || job.state === "running").length} 个进行中</Tag>
        <Text fontSize="$xs" color="$neutral9">
          可以关闭预览或离开此页面，任务会继续；回来后从这里查看结果。
        </Text>
      </HStack>
      <VStack spacing="$2" alignItems="stretch" w="$full">
        <For each={props.jobs}>
          {(job) => (
            <Box
              p="$2"
              rounded="$md"
              border="1px solid $neutral6"
              background={useColorModeValue("$neutral1", "$neutral2")()}
            >
              <HStack spacing="$2" alignItems="center" wrap="wrap">
                <Tag colorScheme={stateColor(job.state)}>{stateText(job.state)}</Tag>
                <Text
                  flex="1"
                  minW="180px"
                  fontSize="$sm"
                  css={{ "word-break": "break-all" }}
                  title={job.path}
                >
                  {job.path.split("/").pop() || job.path}
                </Text>
                <Show when={job.state === "queued" || job.state === "running"}>
                  <Text fontSize="$xs" color="$neutral9">
                    {job.done}/{job.total || 9}
                  </Text>
                  <Button size="xs" colorScheme="danger" variant="outline" onClick={() => props.onCancel(job)}>
                    取消任务
                  </Button>
                </Show>
                <Show when={job.state === "succeeded"}>
                  <Button size="xs" colorScheme="info" onClick={() => props.onOpen(job)}>
                    查看结果
                  </Button>
                </Show>
                <Show when={job.state === "failed"}>
                  <Text fontSize="$xs" color="$danger9" css={{ "word-break": "break-all" }}>
                    {job.error || "生成失败"}
                  </Text>
                </Show>
              </HStack>
              <Show when={job.state === "queued" || job.state === "running"}>
                <Progress mt="$2" value={job.progress || 0} max={100} size="xs">
                  <ProgressIndicator color="$info6" />
                </Progress>
              </Show>
            </Box>
          )}
        </For>
      </VStack>
    </Box>
  </Show>
)
