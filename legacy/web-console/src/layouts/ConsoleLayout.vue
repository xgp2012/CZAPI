<script setup lang="ts">
/**
 * User-centre shell: a fixed ink rail plus a content column.
 *
 * The rail is deliberately dark in both themes — it is the constant that makes
 * the two surfaces feel like one product family while the admin rail carries a
 * different accent loadout.
 */
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useOverviewStore } from '@/stores/overview'
import { fmtMoney, fmtTokens } from '@/utils/format'
import { toggleTheme, theme } from '@/utils/theme'
import { isMock } from '@/api/client'
import Icon from '@/components/Icon.vue'

const route = useRoute()
const overview = useOverviewStore()

const nav = [
  { to: '/', label: '概览', icon: 'gauge' },
  { to: '/keys', label: 'Key 管理', icon: 'key' },
  { to: '/models', label: '模型列表', icon: 'layers' },
  { to: '/usage', label: '用量明细', icon: 'chart' },
  { to: '/docs', label: '接入文档', icon: 'book' },
] as const

const monthTokens = computed(() => {
  const m = overview.data?.month
  return m ? m.prompt_tokens + m.completion_tokens : 0
})

const mock = computed(() => isMock())

onMounted(() => {
  overview.load().catch(() => {
    /* the layout degrades quietly; the page itself surfaces the error */
  })
})
</script>

<template>
  <div class="shell">
    <aside class="rail">
      <div class="rail__brand">
        <span class="rail__mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.1"
            stroke-linecap="round" stroke-linejoin="round">
            <path d="M4 17.5 9.5 6l4 7.5L17 9l3 8.5" />
          </svg>
        </span>
        <span class="rail__wordmark">CZAPI</span>
        <span class="rail__sub">用户中心</span>
      </div>

      <nav class="rail__nav" aria-label="用户中心导航">
        <RouterLink v-for="item in nav" :key="item.to" :to="item.to" class="rail__link"
          :class="{ 'is-active': route.path === item.to }">
          <Icon :name="item.icon" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="rail__spacer" />

      <div class="rail__meter">
        <div class="rail__meter-label">本月 Token</div>
        <div class="rail__meter-value mono">{{ fmtTokens(monthTokens) }}</div>
      </div>

      <div class="rail__foot">
        <RouterLink to="/admin" class="rail__link rail__link--quiet">
          <Icon name="console" />
          <span>管理后台</span>
        </RouterLink>
        <button class="rail__theme" type="button" @click="toggleTheme()">
          <Icon :name="theme === 'dark' ? 'sun' : 'moon'" />
          <span>{{ theme === 'dark' ? '浅色模式' : '深色模式' }}</span>
        </button>
      </div>
    </aside>

    <div class="shell__body">
      <header class="topbar">
        <div class="topbar__id">
          <span class="topbar__email mono">{{ overview.data?.account.email ?? '—' }}</span>
          <span v-if="overview.data" class="topbar__cid mono">client {{ overview.data.account.v10_client_id }}</span>
        </div>
        <div class="topbar__right">
          <span v-if="mock" class="badge badge--info badge--no-dot" title="当前由 src/api/mock.ts 提供数据">
            mock 数据
          </span>
          <div class="topbar__balance">
            <span class="topbar__balance-label">余额</span>
            <span class="topbar__balance-value mono">{{ fmtMoney(overview.data?.balance_micros) }}</span>
          </div>
        </div>
      </header>

      <main class="shell__main">
        <RouterView />
      </main>
    </div>
  </div>
</template>

<style scoped>
.shell {
  display: grid;
  grid-template-columns: 232px minmax(0, 1fr);
  min-height: 100vh;
  background: var(--bg-app);
}

/* ---- rail ------------------------------------------------------- */
.rail {
  position: sticky;
  top: 0;
  height: 100vh;
  display: flex;
  flex-direction: column;
  gap: var(--sp-2);
  padding: var(--sp-5) var(--sp-3) var(--sp-4);
  background: var(--rail-bg);
  border-right: 1px solid rgba(255, 255, 255, 0.06);
}

.rail__brand {
  display: grid;
  grid-template-columns: 28px 1fr;
  grid-template-areas: 'mark word' 'mark sub';
  align-items: center;
  column-gap: var(--sp-2);
  padding: var(--sp-2) var(--sp-3) var(--sp-5);
}

.rail__mark {
  grid-area: mark;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 7px;
  color: var(--accent);
  background: rgba(255, 178, 91, 0.12);
  border: 1px solid rgba(255, 178, 91, 0.22);
}

.rail__wordmark {
  grid-area: word;
  font-size: var(--fs-md);
  font-weight: 640;
  letter-spacing: 0.02em;
  color: var(--rail-ink-strong);
  line-height: 1.1;
}

.rail__sub {
  grid-area: sub;
  font-size: var(--fs-2xs);
  color: var(--rail-ink);
  letter-spacing: 0.08em;
}

.rail__nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.rail__link {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  padding: 8px var(--sp-3);
  border-radius: var(--r-sm);
  color: var(--rail-ink);
  font-size: var(--fs-sm);
  font-weight: 500;
  text-decoration: none;
  border: 1px solid transparent;
  transition: background 140ms ease, color 140ms ease;
}

.rail__link:hover {
  background: rgba(255, 255, 255, 0.045);
  color: var(--rail-ink-strong);
  text-decoration: none;
}

.rail__link.is-active {
  background: var(--rail-active-bg);
  color: var(--rail-active-ink);
  border-color: rgba(255, 178, 91, 0.2);
  font-weight: 600;
}

.rail__spacer {
  flex: 1;
}

.rail__meter {
  margin: 0 var(--sp-2) var(--sp-2);
  padding: var(--sp-3);
  border-radius: var(--r-sm);
  background: rgba(255, 255, 255, 0.035);
  border: 1px solid rgba(255, 255, 255, 0.06);
}

.rail__meter-label {
  font-size: var(--fs-2xs);
  color: var(--rail-ink);
  letter-spacing: 0.06em;
}

.rail__meter-value {
  margin-top: 3px;
  font-size: var(--fs-md);
  color: var(--rail-ink-strong);
}

.rail__foot {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-top: var(--sp-2);
  border-top: 1px solid rgba(255, 255, 255, 0.07);
}

.rail__link--quiet {
  font-size: var(--fs-xs);
}

.rail__theme {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  padding: 7px var(--sp-3);
  border-radius: var(--r-sm);
  border: none;
  background: transparent;
  color: var(--rail-ink);
  font-size: var(--fs-xs);
  cursor: pointer;
  text-align: left;
}

.rail__theme:hover {
  background: rgba(255, 255, 255, 0.045);
  color: var(--rail-ink-strong);
}

/* ---- body ------------------------------------------------------- */
.shell__body {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.topbar {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-4);
  height: 56px;
  padding: 0 var(--sp-10);
  background: color-mix(in srgb, var(--bg-app) 88%, transparent);
  backdrop-filter: blur(10px);
  border-bottom: 1px solid var(--line-1);
}

.topbar__id {
  display: flex;
  align-items: baseline;
  gap: var(--sp-3);
  min-width: 0;
}

.topbar__email {
  font-size: var(--fs-sm);
  color: var(--ink-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.topbar__cid {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.topbar__right {
  display: flex;
  align-items: center;
  gap: var(--sp-4);
}

.topbar__balance {
  display: flex;
  align-items: baseline;
  gap: var(--sp-2);
  padding-left: var(--sp-4);
  border-left: 1px solid var(--line-1);
}

.topbar__balance-label {
  font-size: var(--fs-2xs);
  color: var(--ink-3);
}

.topbar__balance-value {
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--ink-1);
}

.shell__main {
  flex: 1;
  min-width: 0;
}

@media (max-width: 1180px) {
  .shell {
    grid-template-columns: 196px minmax(0, 1fr);
  }
  .topbar {
    padding: 0 var(--sp-6);
  }
}
</style>
