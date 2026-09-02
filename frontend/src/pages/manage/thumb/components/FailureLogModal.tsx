import {
  Box,
  Button,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Text,
  VStack,
  useColorModeValue,
} from "@hope-ui/solid"
import { For, Show } from "solid-js"

export type FailureLogItem = { path: string; msg: string; at: string }

type Props = {
  opened: boolean
  items: FailureLogItem[]
  onClose: () => void
  onClear: () => void
}

export const FailureLogModal = (props: Props) => (
  <Modal opened={props.opened} onClose={props.onClose} size={{ "@initial": "xs", "@md": "lg" }}>
    <ModalOverlay />
    <ModalContent>
      <ModalHeader>失败日志（{props.items.length}）</ModalHeader>
      <ModalBody css={{ maxH: "60vh", overflow: "auto" }}>
        <Show when={props.items.length} fallback={<Text color="$neutral9">暂无失败记录</Text>}>
          <VStack spacing="$2" direction="column" w="$full">
            <For each={props.items}>
              {(item) => (
                <Box
                  p="$2"
                  rounded="$md"
                  border="1px solid $neutral6"
                  w="$full"
                  background={useColorModeValue("$neutral1", "$neutral2")()}
                >
                  <Text fontWeight="$medium" css={{ "word-break": "break-all" }}>
                    {item.path.split("/").pop()}
                  </Text>
                  <Text fontSize="$xs" color="$neutral9" css={{ "word-break": "break-all" }}>
                    {item.path}
                  </Text>
                  <Text fontSize="$sm" color="$danger9" css={{ "word-break": "break-all" }}>
                    原因：{item.msg}
                  </Text>
                  <Show when={item.at}>
                    <Text fontSize="$xs" color="$neutral9">
                      时间：{item.at}
                    </Text>
                  </Show>
                </Box>
              )}
            </For>
          </VStack>
        </Show>
      </ModalBody>
      <ModalFooter display="flex" gap="$2" justifyContent="flex-end">
        <Button colorScheme="danger" onClick={props.onClear}>
          删除失败日志
        </Button>
        <Button colorScheme="neutral" onClick={props.onClose}>
          关闭
        </Button>
      </ModalFooter>
    </ModalContent>
  </Modal>
)
