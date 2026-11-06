<script setup lang="ts">
/** Page header: title, one-line description, actions on the right. */
defineProps<{ title: string; desc?: string }>()
</script>

<template>
  <header class="page-head">
    <div class="page-head__title">
      <h1>{{ title }}</h1>
      <p v-if="desc" class="page-head__desc">{{ desc }}</p>
    </div>
    <div v-if="$slots.actions" class="page-head__actions">
      <slot name="actions" />
    </div>
  </header>
</template>
