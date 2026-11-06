<script setup lang="ts">
/** Copy-to-clipboard button with a transient confirmation. */
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { copyText } from '@/utils/format'
import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{ value: string; label?: string; size?: 'small' | 'default'; text?: boolean }>(),
  { label: '复制', size: 'small', text: true },
)

const done = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null

async function run() {
  const ok = await copyText(props.value)
  if (!ok) {
    ElMessage.error('复制失败，请手动选中复制')
    return
  }
  done.value = true
  ElMessage.success('已复制到剪贴板')
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => (done.value = false), 1600)
}
</script>

<template>
  <el-button :size="size" :text="text" @click="run">
    <span class="copy">
      <Icon :name="done ? 'check' : 'copy'" :size="14" />
      <span>{{ done ? '已复制' : label }}</span>
    </span>
  </el-button>
</template>

<style scoped>
.copy {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
</style>
