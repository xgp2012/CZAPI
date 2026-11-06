import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { consoleApi } from '@/api/console'
import type { ApiKey, ConsoleModel, SubscriptionSummary, CreateKeyRequest, CreateKeyResponse, UpdateKeyRequest } from '@/types/api'

/** Key management state: list, mutations and the one-time plaintext hand-off. */
export const useKeysStore = defineStore('keys', () => {
  const keys = ref<ApiKey[]>([])
  const models = ref<ConsoleModel[]>([])
  const subscriptions = ref<SubscriptionSummary[]>([])
  const loading = ref(false)
  const saving = ref(false)
  const error = ref<string | null>(null)

  /** Held only between "create" and the modal being closed — never persisted. */
  const freshKey = ref<CreateKeyResponse | null>(null)

  const activeCount = computed(() => keys.value.filter((k) => k.status === 'active').length)

  async function load() {
    loading.value = true
    error.value = null
    try {
      const [k, m] = await Promise.all([consoleApi.listKeys(), consoleApi.models()])
      keys.value = k
      models.value = m.items
    } catch (e) {
      error.value = e instanceof Error ? e.message : '加载 Key 列表失败'
      throw e
    } finally {
      loading.value = false
    }
  }

  /** Subscriptions are only needed by the create dialog; reuse the overview cache. */
  function setSubscriptions(subs: SubscriptionSummary[]) {
    subscriptions.value = subs
  }

  async function create(payload: CreateKeyRequest) {
    saving.value = true
    try {
      const res = await consoleApi.createKey(payload)
      freshKey.value = res
      await load()
      return res
    } finally {
      saving.value = false
    }
  }

  async function update(id: number, patch: UpdateKeyRequest) {
    saving.value = true
    try {
      const updated = await consoleApi.updateKey(id, patch)
      const i = keys.value.findIndex((k) => k.key_id === id)
      if (i >= 0) keys.value[i] = updated
      return updated
    } finally {
      saving.value = false
    }
  }

  async function toggle(id: number, next: boolean) {
    return update(id, { status: next ? 'active' : 'suspended' })
  }

  async function revoke(id: number) {
    saving.value = true
    try {
      await consoleApi.revokeKey(id)
      const i = keys.value.findIndex((k) => k.key_id === id)
      if (i >= 0) keys.value[i] = { ...keys.value[i]!, status: 'revoked' }
    } finally {
      saving.value = false
    }
  }

  function clearFreshKey() {
    freshKey.value = null
  }

  return {
    keys,
    models,
    subscriptions,
    loading,
    saving,
    error,
    freshKey,
    activeCount,
    load,
    setSubscriptions,
    create,
    update,
    toggle,
    revoke,
    clearFreshKey,
  }
})
