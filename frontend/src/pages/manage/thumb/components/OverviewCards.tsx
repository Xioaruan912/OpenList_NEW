import {
  Box,
  HStack,
  Switch as HopeSwitch,
  Text,
  VStack,
  useColorModeValue,
} from "@hope-ui/solid"
import { Show } from "solid-js"
import type { ThumbStatus } from "../types"

type Props = {
  status?: ThumbStatus
  onSetAuto: (generate?: boolean, upload?: boolean) => void
}

export const OverviewCards = (props: Props) => (
  <HStack spacing="$2" gap="$2" w="$full" wrap={{ "@initial": "wrap", "@md": "unset" }}>
    <Box
      p="$3"
      rounded="$lg"
      border="1px solid $neutral7"
      background={useColorModeValue("$neutral1", "$neutral2")()}
    >
      缓存 {props.status?.cached_files || 0} 个
      <Text fontSize="$sm" color="$neutral9">
        网盘 {props.status?.cloud_files || 0} · 本地 {props.status?.local_files || 0}
      </Text>
    </Box>
    <Box
      p="$3"
      rounded="$lg"
      border="1px solid $neutral7"
      background={useColorModeValue("$neutral1", "$neutral2")()}
    >
      占用 {((props.status?.cache_size || 0) / 1048576).toFixed(1)} MB
    </Box>
    <Show when={props.status?.metrics}>
      <Box
        p="$3"
        rounded="$lg"
        border="1px solid $neutral7"
        background={useColorModeValue("$neutral1", "$neutral2")()}
      >
        命中率 {(props.status?.metrics?.cache_hit_rate || 0).toFixed(1)}%
        <Text fontSize="$sm" color="$neutral9">
          生成均值 {props.status?.metrics?.avg_generate_ms || 0}ms · P95{" "}
          {props.status?.metrics?.p95_generate_ms || 0}ms
        </Text>
        <Text fontSize="$xs" color="$neutral9">
          Range URL {props.status?.metrics?.range_http || 0} · Reader {props.status?.metrics?.range_reader || 0} ·
          Gateway {props.status?.metrics?.range_gateway || 0}
        </Text>
        <Show when={Object.keys(props.status?.metrics?.failures || {}).length > 0}>
          <Text fontSize="$xs" color="$neutral9">
            失败分类{" "}
            {Object.entries(props.status?.metrics?.failures || {})
              .sort((a, b) => b[1] - a[1])
              .map(([kind, count]) => `${kind} ${count}`)
              .join(" · ")}
          </Text>
        </Show>
      </Box>
    </Show>
    <Show when={props.status?.cache_dir}>
      <Box
        p="$3"
        rounded="$lg"
        border="1px solid $neutral7"
        background={useColorModeValue("$neutral1", "$neutral2")()}
      >
        缓存目录 {props.status!.cache_dir}
      </Box>
    </Show>
    <Box
      p="$3"
      rounded="$lg"
      border="1px solid $neutral7"
      background={useColorModeValue("$neutral1", "$neutral2")()}
    >
      <VStack spacing="$1" alignItems="start">
        <HopeSwitch
          checked={!!props.status?.prewarm_enabled}
          onChange={(e: Event) => props.onSetAuto((e.currentTarget as HTMLInputElement).checked, undefined)}
        >
          自动生成
        </HopeSwitch>
        <HopeSwitch
          checked={!!props.status?.auto_upload}
          onChange={(e: Event) => props.onSetAuto(undefined, (e.currentTarget as HTMLInputElement).checked)}
        >
          自动上传
        </HopeSwitch>
      </VStack>
    </Box>
  </HStack>
)
