import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { adminApi } from '@/api/admin'
import { getStoredToken, isMock, isSignedOut, markSignOut, setMockMode, setStoredToken } from '@/api/client'
import type { AdminUser } from '@/types/api'

const ADMIN_KEY = 'czapi.admin.profile'

function readProfile(): AdminUser | null {
  try {
    const raw = window.localStorage.getItem(ADMIN_KEY)
    return raw ? (JSON.parse(raw) as AdminUser) : null
  } catch {
    return null
  }
}

/**
 * With no backend there is nothing to authenticate against, so mock mode treats
 * the admin surface as already entered and shows a demo identity. Signing out
 * still works — it leaves a marker that suppresses the auto-session until the
 * next login. With VITE_USE_MOCK=false none of this applies.
 */
const DEMO_ADMIN: AdminUser = { id: 1, username: 'admin', role: 'super_admin · mock' }

function bootstrapMockSession() {
  if (!isMock() || isSignedOut()) return
  if (!getStoredToken()) setStoredToken('mock.jwt.demo')
  if (!window.localStorage.getItem(ADMIN_KEY)) {
    window.localStorage.setItem(ADMIN_KEY, JSON.stringify(DEMO_ADMIN))
  }
}

bootstrapMockSession()

export const useAuthStore = defineStore('auth', () => {
  const admin = ref<AdminUser | null>(readProfile())
  const token = ref<string>(getStoredToken())
  const loading = ref(false)

  const isAuthenticated = computed(() => Boolean(token.value))
  const mockMode = computed(() => isMock())

  /** In mock mode any username + a 4-char password signs in. */
  async function login(username: string, password: string) {
    loading.value = true
    try {
      const res = await adminApi.login(username, password)
      token.value = res.token
      admin.value = res.admin
      setStoredToken(res.token)
      markSignOut(false)
      window.localStorage.setItem(ADMIN_KEY, JSON.stringify(res.admin))
      return res
    } finally {
      loading.value = false
    }
  }

  async function logout() {
    try {
      await adminApi.logout()
    } catch {
      /* logging out locally must never be blocked by a failed call */
    }
    token.value = ''
    admin.value = null
    setStoredToken('')
    window.localStorage.removeItem(ADMIN_KEY)
    // Remember the intent so the mock bootstrap does not silently sign us back in.
    if (isMock()) markSignOut(true)
  }

  /** One-click entry from the login screen when running against the mock layer. */
  function demoSignIn() {
    setMockMode(true)
    markSignOut(false)
    token.value = 'mock.jwt.demo'
    admin.value = DEMO_ADMIN
    setStoredToken('mock.jwt.demo')
    window.localStorage.setItem(ADMIN_KEY, JSON.stringify(DEMO_ADMIN))
  }

  return { admin, token, loading, isAuthenticated, mockMode, login, logout, demoSignIn }
})
