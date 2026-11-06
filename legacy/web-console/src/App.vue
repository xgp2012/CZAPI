<script setup lang="ts">
/**
 * App shell.
 *
 * The two surfaces share one SPA. Rather than nesting a router-view twice, the
 * root decides which layout to render from the resolved path: anything under
 * `/admin` gets AdminLayout, everything else gets ConsoleLayout. The layouts
 * each own their own sidebar and render the active route in a nested
 * router-view, so switching surfaces feels like a different application without
 * a second bundle or a page reload.
 */
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import ConsoleLayout from '@/layouts/ConsoleLayout.vue'
import AdminLayout from '@/layouts/AdminLayout.vue'

const route = useRoute()
const isAdmin = computed(() => route.path === '/admin' || route.path.startsWith('/admin/'))
const isAdminLogin = computed(() => route.name === 'admin-login')
</script>

<template>
  <RouterView v-if="isAdminLogin" />
  <component :is="isAdmin ? AdminLayout : ConsoleLayout" v-else />
</template>
