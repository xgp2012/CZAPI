<script setup lang="ts">
/**
 * Tiny inline icon set — 20×20 stroke glyphs drawn on a 24 grid.
 * No icon-font dependency: every glyph is a path here so the bundle stays lean
 * and the stroke weight can stay consistent with the hairline UI.
 */
const props = withDefaults(defineProps<{ name: string; size?: number }>(), { size: 17 })

const paths: Record<string, string> = {
  gauge: 'M12 14a2 2 0 1 0 0-4 2 2 0 0 0 0 4Zm1.5-2.9 3-4.6M20 17a9 9 0 1 0-16 0',
  key: 'M14.5 9.5a3.5 3.5 0 1 1-3-3.4M14 10l6 6v3h-3l-1-1v-2h-2l-2-2M4 20l5.5-5.5',
  layers: 'M12 3 3 8l9 5 9-5-9-5Zm9 9-9 5-9-5m18 4.5-9 5-9-5',
  chart: 'M4 20V4m0 16h16M8 16v-5m4 5V8m4 8v-6',
  book: 'M4 5.5A2.5 2.5 0 0 1 6.5 3H20v14H6.5A2.5 2.5 0 0 0 4 19.5v-14Zm0 14A2.5 2.5 0 0 1 6.5 17H20v4H6.5A2.5 2.5 0 0 1 4 19.5Z',
  console: 'M3 5h18v14H3zM7 10l2.5 2.5L7 15m5 0h4',
  sun: 'M12 4V2m0 20v-2m8-8h2M2 12h2m13.7-5.7 1.4-1.4M4.9 19.1l1.4-1.4m11.4 0 1.4 1.4M4.9 4.9l1.4 1.4M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0Z',
  moon: 'M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5Z',
  copy: 'M9 9h9v11H9zM6 15H4V4h11v2',
  check: 'M4 12.5 9 17.5 20 6.5',
  plus: 'M12 5v14M5 12h14',
  refresh: 'M20 12a8 8 0 1 1-2.4-5.7M20 4v4h-4',
  search: 'M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13Zm5-1.5L20 20',
  external: 'M14 4h6v6M20 4l-8 8M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5',
  bolt: 'M13 3 5 13h5l-1 8 8-10h-5l1-8Z',
  shield: 'M12 3 5 6v6c0 4.5 3 7.5 7 9 4-1.5 7-4.5 7-9V6l-7-3Z',
  money: 'M3 7h18v10H3zM12 12a2 2 0 1 0 0-1 2 2 0 0 0 0 1Zm-6-2h1m10 4h1',
  clock: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Zm0-13v5l3 2',
  server: 'M4 5h16v5H4zM4 14h16v5H4zM7 7.5h.01M7 16.5h.01',
  alert: 'M12 4 3 20h18L12 4Zm0 5v5m0 3h.01',
  download: 'M12 4v10m0 0 4-4m-4 4-4-4M4 19h16',
  close: 'M6 6l12 12M18 6 6 18',
  chevronRight: 'M9 5l7 7-7 7',
  chevronDown: 'M5 9l7 7 7-7',
  info: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Zm0-13v.01M12 11v5',
  edit: 'M4 20h4L20 8l-4-4L4 16v4Zm10-14 4 4',
  trash: 'M4 7h16M9 7V4h6v3m-8 0 1 13h8l1-13',
  ban: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18ZM6 6l12 12',
  play: 'M8 5l11 7-11 7V5Z',
  pause: 'M9 5v14M15 5v14',
  rotate: 'M4 12a8 8 0 0 1 13.7-5.7M20 12a8 8 0 0 1-13.7 5.7M18 3v4h-4M6 21v-4h4',
  users: 'M16 19v-1a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v1m6.5-9a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7ZM21 19v-1a4 4 0 0 0-3-3.9M16.5 4.1a3.5 3.5 0 0 1 0 6.8',
  receipt: 'M6 3h12v18l-3-2-3 2-3-2-3 2V3Zm3 5h6m-6 4h6',
  scale: 'M12 4v16M6 8h12M6 8 3 15h6L6 8Zm12 0-3 7h6l-3-7Z',
  toggle: 'M8 7h8a5 5 0 0 1 0 10H8A5 5 0 0 1 8 7Zm4 5h.01',
  scroll: 'M6 3h12v18l-3-2-3 2-3-2-3 2V3Zm3 4h6m-6 4h6m-6 4h3',
  gear: 'M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Zm8-3.5a8 8 0 0 0-.2-1.6l2-1.4-2-3.4-2.2 1a8 8 0 0 0-2.7-1.6L14.5 3h-5l-.4 2.5a8 8 0 0 0-2.7 1.6l-2.2-1-2 3.4 2 1.4a8 8 0 0 0 0 3.2l-2 1.4 2 3.4 2.2-1a8 8 0 0 0 2.7 1.6l.4 2.5h5l.4-2.5a8 8 0 0 0 2.7-1.6l2.2 1 2-3.4-2-1.4c.1-.5.2-1 .2-1.6Z',
  filter: 'M4 5h16l-6 7v7l-4-2v-5L4 5Z',
  logout: 'M9 20H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h4m4 0h6a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-6m0-4 3-3-3-3m3 3H8',
}

const d = () => paths[props.name] ?? paths.info!
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path :d="d()" />
  </svg>
</template>
