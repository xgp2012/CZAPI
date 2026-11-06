<script setup lang="ts">
/**
 * Quota meter.
 *
 * The bar is a gauge, not decoration: it inverts the usual "more is better"
 * reading because here the filled portion is quota *consumed*. It starts in
 * amber (on-brand for spend), turns amber-warm at 70% and red at 90% so an
 * account running out is visible from across the room.
 */
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    used: number
    total: number
    height?: number
    /** Show "12.3 / 100 万" style labels beneath the bar. */
    showLabels?: boolean
    unit?: string
  }>(),
  { height: 8, showLabels: false, unit: 'token' },
)

const pct = computed(() => {
  if (!props.total || props.total <= 0) return 0
  return Math.min(100, Math.max(0, (props.used / props.total) * 100))
})

const tone = computed(() => (pct.value >= 90 ? 'danger' : pct.value >= 70 ? 'warn' : 'ok'))

const fmt = (n: number) => {
  if (n >= 1e8) return `${(n / 1e8).toFixed(2)} 亿`
  if (n >= 1e4) return `${(n / 1e4).toFixed(2)} 万`
  return new Intl.NumberFormat('zh-CN').format(Math.round(n))
}
</script>

<template>
  <div class="quota">
    <div class="quota__track meter" :style="{ height: `${height}px` }" role="progressbar"
      :aria-valuenow="Math.round(pct)" aria-valuemin="0" aria-valuemax="100">
      <span class="meter__fill" :class="`meter__fill--${tone}`" :style="{ width: `${pct}%` }" />
    </div>
    <div v-if="showLabels" class="quota__labels">
      <span class="quota__used mono">{{ fmt(used) }}</span>
      <span class="quota__sep">/</span>
      <span class="quota__total mono">{{ fmt(total) }}</span>
      <span class="quota__pct" :class="`quota__pct--${tone}`">{{ pct.toFixed(1) }}%</span>
    </div>
  </div>
</template>

<style scoped>
.quota {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 100px;
}

.quota__labels {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: var(--fs-2xs);
  color: var(--ink-3);
}

.quota__used {
  color: var(--ink-2);
}

.quota__sep {
  color: var(--ink-4);
}

.quota__total {
  color: var(--ink-4);
}

.quota__pct {
  margin-left: auto;
  font-family: var(--font-mono);
  font-weight: 600;
}

.quota__pct--ok {
  color: var(--ink-2);
}

.quota__pct--warn {
  color: var(--warn);
}

.quota__pct--danger {
  color: var(--danger);
}
</style>
