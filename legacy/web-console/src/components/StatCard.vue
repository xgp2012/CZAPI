<script setup lang="ts">
/** A KPI tile: label, mono figure, optional delta/caption and footer slot. */
withDefaults(
  defineProps<{
    label: string
    value: string
    hint?: string
    tone?: 'plain' | 'accent' | 'danger' | 'ok'
    mono?: boolean
  }>(),
  { tone: 'plain', mono: true },
)
</script>

<template>
  <div class="stat" :class="`stat--${tone}`">
    <div class="stat__label">{{ label }}</div>
    <div class="stat__value" :class="{ mono }">{{ value }}</div>
    <div v-if="hint" class="stat__hint">{{ hint }}</div>
    <slot />
  </div>
</template>

<style scoped>
.stat {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: var(--sp-4) var(--sp-5) var(--sp-4);
  background: var(--bg-panel);
  border: 1px solid var(--line-1);
  border-radius: var(--r-md);
  min-width: 0;
  position: relative;
  overflow: hidden;
}

.stat::before {
  /* A 2px top rule encodes tone without tinting the whole card. */
  content: '';
  position: absolute;
  inset: 0 0 auto 0;
  height: 2px;
  background: transparent;
}

.stat--accent::before {
  background: var(--accent);
}

.stat--danger::before {
  background: var(--danger);
}

.stat--ok::before {
  background: var(--ok);
}

.stat__label {
  font-size: var(--fs-xs);
  color: var(--ink-3);
  font-weight: 500;
  letter-spacing: 0.01em;
}

.stat__value {
  font-size: var(--fs-xl);
  line-height: 1.15;
  font-weight: 600;
  color: var(--ink-1);
  letter-spacing: -0.015em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.stat--accent .stat__value {
  color: var(--accent);
}

.stat--danger .stat__value {
  color: var(--danger);
}

.stat__hint {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}
</style>
