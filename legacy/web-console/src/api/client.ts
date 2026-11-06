/**
 * HTTP client + the mock/real switch.
 *
 * The switch is deliberately simple and non-magical:
 *   mock is used when  VITE_USE_MOCK !== 'false'  (default true)
 *               or  when no admin token is present for an /admin/v1 call.
 *
 * Every request unwraps the frozen envelope `{code,msg,data}`; a non-zero code
 * is turned into a rejected promise carrying the message so callers can toast it.
 */

import axios, { type AxiosRequestConfig, type AxiosResponse } from 'axios'
import type { Envelope } from '@/types/api'

const envUseMock = (import.meta.env.VITE_USE_MOCK ?? 'true').toString().toLowerCase()
const apiBase = import.meta.env.VITE_API_BASE ?? ''

const TOKEN_KEY = 'czapi.admin.token'
/** Set after an explicit sign-out in mock mode, so the demo session is not re-seeded. */
const SIGNED_OUT_KEY = 'czapi.admin.signedOut'

/** Non-zero `code` from the gateway, surfaced to the UI as a normal Error. */
export class ApiError extends Error {
  code: number
  constructor(code: number, msg: string) {
    super(msg)
    this.code = code
    this.name = 'ApiError'
  }
}

/* ---------------------------------------------------------------- */
/* Mock switch                                                       */
/* ---------------------------------------------------------------- */

let mockOverride: boolean | null = null

/** Force mock on/off at runtime (the login screen and README ride on this). */
export function setMockMode(on: boolean | null) {
  mockOverride = on
}

export function getStoredToken(): string {
  const stored = window.localStorage.getItem(TOKEN_KEY) ?? import.meta.env.VITE_ADMIN_TOKEN ?? ''
  // In mock mode there is no real session to obtain, so a placeholder token keeps
  // the router guard and the axios interceptor on the happy path — unless the
  // operator explicitly signed out, which must stay signed out until they log in.
  if (!stored && isMock() && window.localStorage.getItem(SIGNED_OUT_KEY) !== '1') return 'mock.jwt.demo'
  return stored
}

export function isSignedOut(): boolean {
  return window.localStorage.getItem(SIGNED_OUT_KEY) === '1'
}

export function markSignOut(on: boolean) {
  if (on) window.localStorage.setItem(SIGNED_OUT_KEY, '1')
  else window.localStorage.removeItem(SIGNED_OUT_KEY)
}

export function setStoredToken(token: string) {
  if (token) window.localStorage.setItem(TOKEN_KEY, token)
  else window.localStorage.removeItem(TOKEN_KEY)
}

/** True when the request should be answered locally by src/api/mock.ts. */
export function isMock(): boolean {
  if (mockOverride !== null) return mockOverride
  if (envUseMock === 'false') return false
  return true
}

/** Mock is on, but the admin surface has no token: the UI stays usable. */
export function isMockForcedByMissingToken(adminCall: boolean): boolean {
  return adminCall && !getStoredToken() && envUseMock !== 'false'
}

/* ---------------------------------------------------------------- */
/* Axios                                                            */
/* ---------------------------------------------------------------- */

export const http = axios.create({
  baseURL: apiBase,
  timeout: 30_000,
  withCredentials: true, // the user centre rides on the HttpOnly `cz_ss` cookie
  headers: { 'Content-Type': 'application/json' },
})

http.interceptors.request.use((config) => {
  const token = getStoredToken()
  const url = config.url ?? ''
  if (token && url.startsWith('/admin/')) {
    config.headers.set('Authorization', `Bearer ${token}`)
  }
  return config
})

http.interceptors.response.use(
  (res) => res,
  (error) => {
    if (axios.isAxiosError(error)) {
      const status = error.response?.status
      const body = error.response?.data as Envelope<unknown> | undefined
      if (body && typeof body.code === 'number' && body.code !== 0) {
        return Promise.reject(new ApiError(body.code, body.msg || `请求失败（${body.code}）`))
      }
      if (status === 401) return Promise.reject(new ApiError(401, '登录已过期，请重新登录'))
      if (status === 403) return Promise.reject(new ApiError(403, '没有权限执行该操作'))
      if (status === 404) return Promise.reject(new ApiError(404, '资源不存在'))
      if (status === 429) return Promise.reject(new ApiError(429, '请求过于频繁，请稍后再试'))
      if (status && status >= 500) return Promise.reject(new ApiError(status, '网关内部错误，请稍后重试'))
      if (error.code === 'ECONNABORTED') return Promise.reject(new ApiError(0, '请求超时'))
      return Promise.reject(new ApiError(0, error.message || '网络异常'))
    }
    return Promise.reject(error)
  },
)

/** Unwrap `{code,msg,data}` and throw on a non-zero code. */
export function unwrap<T>(res: AxiosResponse<Envelope<T>>): T {
  const body = res.data
  if (!body || typeof body.code !== 'number') {
    throw new ApiError(0, '响应格式异常')
  }
  if (body.code !== 0) throw new ApiError(body.code, body.msg || `请求失败（${body.code}）`)
  return body.data
}

export async function get<T>(url: string, config?: AxiosRequestConfig): Promise<T> {
  return unwrap<T>(await http.get<Envelope<T>>(url, config))
}

export async function post<T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
  return unwrap<T>(await http.post<Envelope<T>>(url, data, config))
}

export async function patch<T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
  return unwrap<T>(await http.patch<Envelope<T>>(url, data, config))
}

export async function put<T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
  return unwrap<T>(await http.put<Envelope<T>>(url, data, config))
}

export async function del<T>(url: string, config?: AxiosRequestConfig): Promise<T> {
  return unwrap<T>(await http.delete<Envelope<T>>(url, config))
}
