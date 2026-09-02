import { Button, HStack, Input, Text, VStack } from "@hope-ui/solid"
import { Show } from "solid-js"

type Props = {
  items: { dir: string; count: number }[]
  oldPrefix: string
  newPrefix: string
  onOldPrefix: (value: string) => void
  onNewPrefix: (value: string) => void
  onMigrate: () => void
}

export const StalePathMigration = (props: Props) => (
  <Show when={props.items.length > 0}>
    <VStack spacing="$2" mt="$3" w="$full" direction="column">
      <Text fontWeight="$medium">检测到旧挂载路径（存储挂载路径已变更）：</Text>
      <Text fontSize="$sm">{props.items.map((item) => `${item.dir}（${item.count}）`).join("、")}</Text>
      <HStack direction={{ "@initial": "column", "@md": "row" }} spacing="$2" alignItems="center">
        <Text>旧前缀</Text>
        <Input
          value={props.oldPrefix}
          onInput={(event) => props.onOldPrefix(event.currentTarget.value)}
          placeholder="如 /影视相关"
          w="$full"
          maxW="260px"
        />
        <Text>→ 新前缀</Text>
        <Input
          value={props.newPrefix}
          onInput={(event) => props.onNewPrefix(event.currentTarget.value)}
          placeholder="如 /01_影视相关"
          w="$full"
          maxW="260px"
        />
        <Button colorScheme="info" onClick={props.onMigrate}>
          迁移
        </Button>
      </HStack>
    </VStack>
  </Show>
)
