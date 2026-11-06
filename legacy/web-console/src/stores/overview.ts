import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { consoleApi } from '@/api/console'
import type { Overview } from '@/types/api'

/** Cached console overview: shared by the overview page and the shell badge. */
export const useOverviewStore = defineStore('overview', () => {
  const data = ref<Overview | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  const balanceMicros = computed(() => data.value?.balance_micros ?? 0)

  async function load(force = false) {
    if (data.value && !force) return data.value
    loading.value = true
    error.value = null
    try {
      data.value = await consoleApi.overview()
      return data.value
    } catch (e) {
      error.value = e instanceof Error ? e.message : '加载概览失败'
      throw e
    } finally {
      loading.value = false
    }
  }

  function refresh() {
    return load(true)
  }

  return { data, loading, error, balanceMicros, load, refresh }
})
