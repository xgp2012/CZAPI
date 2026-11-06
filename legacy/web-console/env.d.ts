/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "true" (default) serves every screen from the in-browser mock layer. */
  readonly VITE_USE_MOCK?: string
  /** Gateway origin for real calls; empty means same origin. */
  readonly VITE_API_BASE?: string
  /** Admin JWT used when the mock layer is off. */
  readonly VITE_ADMIN_TOKEN?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>
  export default component
}
