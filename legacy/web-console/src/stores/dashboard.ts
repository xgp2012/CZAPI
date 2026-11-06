import { defineStore } from 'pinia'
import { ref } from 'vue'
import { adminApi } from '@/api/admin'
import type { Dashboard } from '@/types/api'

/** Admin dashboard KPI cache. */
export const useDashboardStore = defineStore('dashboard', () => {
  const data = ref<Dashboard | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const lastLoadedAt = ref<number | null>(null)

  async function load(force = false) {
    if (data.value && !force) return data.value
    loading.value = true
    error.value = null
    try {
      data.value = await adminApi.dashboard()
      lastLoadedAt.value = Date.now()
      return data.value
    } catch (e) {
      error.value = e instanceof Error ? e.message : '加载仪表盘失败'
      throw e
    } finally {
      loading.value = false
    }
  }

  return { data, loading, error, lastLoadedAt, load }
})
