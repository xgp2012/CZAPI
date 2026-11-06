<script setup lang="ts">
/** Shared empty state: says what is missing and what to do next. */
withDefaults(
  defineProps<{
    title: string
    desc?: string
    icon?: string
    compact?: boolean
  }>(),
  { icon: 'info', compact: false },
)
import Icon from './Icon.vue'
</script>

<template>
  <div class="empty" :class="{ 'empty--compact': compact }">
    <span class="empty__icon"><Icon :name="icon" :size="22" /></span>
    <p class="empty__title">{{ title }}</p>
    <p v-if="desc" class="empty__desc">{{ desc }}</p>
    <div v-if="$slots.default" class="empty__action"><slot /></div>
  </div>
</template>

<style scoped>
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--sp-2);
  padding: var(--sp-12) var(--sp-6);
  text-align: center;
}

.empty--compact {
  padding: var(--sp-8) var(--sp-4);
}

.empty__icon {
  display: grid;
  place-items: center;
  width: 42px;
  height: 42px;
  border-radius: 50%;
  background: var(--bg-inset);
  border: 1px solid var(--line-1);
  color: var(--ink-4);
  margin-bottom: var(--sp-1);
}

.empty__title {
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--ink-2);
}

.empty__desc {
  font-size: var(--fs-xs);
  color: var(--ink-3);
  max-width: 46ch;
  line-height: var(--lh-normal);
}

.empty__action {
  margin-top: var(--sp-3);
}
</style>
