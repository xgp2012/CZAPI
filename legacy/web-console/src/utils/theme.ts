import { ref } from 'vue'

export type ThemeName = 'dark' | 'light'

const STORAGE_KEY = 'czapi.theme'

function readInitial(): ThemeName {
  if (typeof window === 'undefined') return 'dark'
  const saved = window.localStorage.getItem(STORAGE_KEY)
  return saved === 'light' ? 'light' : 'dark'
}

export const theme = ref<ThemeName>(readInitial())

export function applyTheme(next: ThemeName) {
  theme.value = next
  const root = document.documentElement
  root.setAttribute('data-theme', next)
  // Element Plus switches its own variable set on `.dark`.
  root.classList.toggle('dark', next === 'dark')
  window.localStorage.setItem(STORAGE_KEY, next)
}

export function toggleTheme() {
  applyTheme(theme.value === 'dark' ? 'light' : 'dark')
}

applyTheme(theme.value)

/**
 * Chart colours mirror the CSS custom properties in src/styles/tokens.css.
 * ECharts needs concrete values, so they are duplicated here (and only here).
 */
export function chartPalette() {
  const dark = theme.value === 'dark'
  return {
    axis: dark ? '#5E7488' : '#8B9AAB',
    split: dark ? 'rgba(255,255,255,.06)' : 'rgba(16,24,32,.07)',
    accent: dark ? '#FFB25B' : '#B65F19',
    accentFill: dark ? 'rgba(255,178,91,.16)' : 'rgba(182,95,25,.12)',
    secondary: dark ? '#6FB4FF' : '#2563A8',
    secondaryFill: dark ? 'rgba(111,180,255,.10)' : 'rgba(37,99,168,.08)',
    text: dark ? '#E7EEF5' : '#0E1822',
    tooltipBg: dark ? '#101A24' : '#FFFFFF',
    tooltipBorder: dark ? '#22323F' : '#DFE5EC',
  }
}
