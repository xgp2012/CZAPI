<script setup lang="ts">
/**
 * Admin shell.
 *
 * Same architecture as the console shell but with a denser rail, a persistent
 * mock-mode banner and a reconcile status chip in the top bar so an operator
 * never has to open the dashboard to know whether books balance.
 */
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '@/stores/auth'
import { fmtDateTime } from '@/utils/format'
import { toggleTheme, theme } from '@/utils/theme'
import { isMock } from '@/api/client'
import Icon from '@/components/Icon.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const groups = [
  {
    title: '运行',
    items: [
      { to: '/admin', label: '仪表盘', icon: 'gauge' },
      { to: '/admin/channels', label: '渠道管理', icon: 'server' },
      { to: '/admin/models', label: '模型与定价', icon: 'layers' },
    ],
  },
  {
    title: '租户',
    items: [
      { to: '/admin/accounts', label: '账户与 Key', icon: 'users' },
      { to: '/admin/subscriptions', label: '订阅', icon: 'clock' },
      { to: '/admin/plans', label: '套餐', icon: 'shield' },
    ],
  },
  {
    title: '财务',
    items: [
      { to: '/admin/ledger', label: '财务流水', icon: 'receipt' },
      { to: '/admin/reconcile', label: '对账', icon: 'scale' },
    ],
  },
  {
    title: '系统',
    items: [
      { to: '/admin/flags', label: '功能开关', icon: 'toggle' },
      { to: '/admin/audit', label: '审计日志', icon: 'scroll' },
      { to: '/admin/system', label: '系统设置', icon: 'gear' },
    ],
  },
] as const

const mock = computed(() => isMock())
const collapsed = ref(false)

const isActive = (to: string) => (to === '/admin' ? route.path === '/admin' : route.path.startsWith(to))

const currentTitle = computed(() => (route.meta.title as string | undefined) ?? '管理后台')
const now = ref(new Date())
onMounted(() => {
  setInterval(() => (now.value = new Date()), 30_000)
})

async function signOut() {
  await auth.logout()
  ElMessage.success('已退出登录')
  router.push('/admin/login')
}
</script>

<template>
  <div class="admin" :class="{ 'admin--collapsed': collapsed }">
    <aside class="arail">
      <div class="arail__brand">
        <span class="arail__mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="2.1"
            stroke-linecap="round" stroke-linejoin="round">
            <path d="M4 17.5 9.5 6l4 7.5L17 9l3 8.5" />
          </svg>
        </span>
        <div v-if="!collapsed" class="arail__brandtext">
          <span class="arail__wordmark">CZAPI</span>
          <span class="arail__sub">管理后台</span>
        </div>
      </div>

      <nav class="arail__nav" aria-label="管理后台导航">
        <div v-for="g in groups" :key="g.title" class="arail__group">
          <div v-if="!collapsed" class="arail__group-title">{{ g.title }}</div>
          <RouterLink v-for="item in g.items" :key="item.to" :to="item.to" class="arail__link"
            :class="{ 'is-active': isActive(item.to) }" :title="collapsed ? item.label : undefined">
            <Icon :name="item.icon" />
            <span v-if="!collapsed">{{ item.label }}</span>
          </RouterLink>
        </div>
      </nav>

      <div class="arail__spacer" />

      <button class="arail__collapse" type="button" @click="collapsed = !collapsed">
        <Icon :name="collapsed ? 'chevronRight' : 'chevronDown'" :size="14" />
        <span v-if="!collapsed">收起导航</span>
      </button>
      <RouterLink to="/" class="arail__link arail__link--quiet">
        <Icon name="external" />
        <span v-if="!collapsed">用户中心</span>
      </RouterLink>
    </aside>

    <div class="admin__body">
      <header class="abar">
        <div class="abar__left">
          <span class="abar__crumb">管理后台</span>
          <Icon name="chevronRight" :size="13" class="abar__divider" />
          <span class="abar__title">{{ currentTitle }}</span>
        </div>

        <div class="abar__right">
          <span v-if="mock" class="badge badge--info badge--no-dot">mock 数据 · 无后端</span>
          <span class="abar__clock mono">{{ now.toLocaleTimeString('zh-CN', { hour12: false }) }}</span>
          <span v-if="auth.admin" class="abar__who">
            <Icon name="shield" :size="14" />
            <span class="mono">{{ auth.admin.username }}</span>
            <span class="abar__role">{{ auth.admin.role }}</span>
          </span>
          <el-button text size="small" @click="toggleTheme()">
            <Icon :name="theme === 'dark' ? 'sun' : 'moon'" :size="15" />
          </el-button>
          <el-button text size="small" @click="signOut">
            <Icon name="logout" :size="15" />
            <span class="abar__logout">退出</span>
          </el-button>
        </div>
      </header>

      <main class="admin__main">
        <RouterView />
      </main>

      <footer class="admin__foot">
        <span>CZAPI 网关控制台 · 契约 {{ 'console-api-v1' }} · 收款与卡密在 V10 侧完成</span>
        <span class="mono">渲染于 {{ fmtDateTime(Math.floor(now.getTime() / 1000)) }}</span>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.admin {
  display: grid;
  grid-template-columns: 236px minmax(0, 1fr);
  min-height: 100vh;
  background: var(--bg-app);
  transition: grid-template-columns 180ms ease;
}

.admin--collapsed {
  grid-template-columns: 64px minmax(0, 1fr);
}

/* ---- rail ------------------------------------------------------- */
.arail {
  position: sticky;
  top: 0;
  height: 100vh;
  display: flex;
  flex-direction: column;
  gap: var(--sp-2);
  padding: var(--sp-4) var(--sp-2) var(--sp-3);
  background: var(--rail-bg);
  border-right: 1px solid rgba(255, 255, 255, 0.06);
  overflow-y: auto;
}

.arail__brand {
  display: flex;
  align-items: center;
  gap: var(--sp-2);
  padding: var(--sp-2) var(--sp-2) var(--sp-4);
}

.arail__mark {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  flex: 0 0 28px;
  border-radius: 7px;
  color: var(--accent);
  background: rgba(255, 178, 91, 0.12);
  border: 1px solid rgba(255, 178, 91, 0.22);
}

.arail__brandtext {
  display: flex;
  flex-direction: column;
  line-height: 1.15;
}

.arail__wordmark {
  font-size: var(--fs-base);
  font-weight: 640;
  letter-spacing: 0.02em;
  color: var(--rail-ink-strong);
}

.arail__sub {
  font-size: var(--fs-2xs);
  color: var(--rail-ink);
  letter-spacing: 0.08em;
}

.arail__nav {
  display: flex;
  flex-direction: column;
  gap: var(--sp-4);
}

.arail__group {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.arail__group-title {
  padding: 0 var(--sp-3) 4px;
  font-size: var(--fs-2xs);
  color: var(--rail-ink);
  opacity: 0.62;
  letter-spacing: 0.08em;
}

.arail__link {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px var(--sp-3);
  border-radius: var(--r-sm);
  color: var(--rail-ink);
  font-size: var(--fs-sm);
  font-weight: 500;
  text-decoration: none;
  border: 1px solid transparent;
  white-space: nowrap;
}

.arail__link:hover {
  background: rgba(255, 255, 255, 0.045);
  color: var(--rail-ink-strong);
  text-decoration: none;
}

.arail__link.is-active {
  background: var(--rail-active-bg);
  color: var(--rail-active-ink);
  border-color: rgba(255, 178, 91, 0.2);
  font-weight: 600;
}

.arail__spacer {
  flex: 1;
}

.arail__collapse {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px var(--sp-3);
  border: none;
  background: transparent;
  color: var(--rail-ink);
  font-size: var(--fs-2xs);
  cursor: pointer;
  border-radius: var(--r-sm);
}

.arail__collapse:hover {
  background: rgba(255, 255, 255, 0.045);
  color: var(--rail-ink-strong);
}

.arail__link--quiet {
  font-size: var(--fs-xs);
  border-top: 1px solid rgba(255, 255, 255, 0.07);
  border-radius: 0;
  margin-top: 4px;
  padding-top: 10px;
}

/* ---- body ------------------------------------------------------- */
.admin__body {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.abar {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-4);
  height: 54px;
  padding: 0 var(--sp-8);
  background: color-mix(in srgb, var(--bg-app) 90%, transparent);
  backdrop-filter: blur(10px);
  border-bottom: 1px solid var(--line-1);
}

.abar__left {
  display: flex;
  align-items: center;
  gap: var(--sp-2);
  min-width: 0;
}

.abar__crumb {
  font-size: var(--fs-xs);
  color: var(--ink-4);
}

.abar__divider {
  color: var(--ink-4);
}

.abar__title {
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--ink-1);
}

.abar__right {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
}

.abar__clock {
  font-size: var(--fs-xs);
  color: var(--ink-4);
}

.abar__who {
  display: flex;
  align-items: center;
  gap: 6px;
  padding-left: var(--sp-3);
  border-left: 1px solid var(--line-1);
  font-size: var(--fs-xs);
  color: var(--ink-2);
}

.abar__role {
  padding: 0 5px;
  border-radius: var(--r-xs);
  background: var(--bg-inset);
  color: var(--ink-3);
  font-size: var(--fs-2xs);
}

.abar__logout {
  margin-left: 4px;
  font-size: var(--fs-xs);
}

.admin__main {
  flex: 1;
  min-width: 0;
}

.admin__foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-4);
  padding: var(--sp-4) var(--sp-10);
  border-top: 1px solid var(--line-1);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  flex-wrap: wrap;
}

@media (max-width: 1320px) {
  .abar {
    padding: 0 var(--sp-6);
  }
  .admin__foot {
    padding: var(--sp-4) var(--sp-6);
  }
}
</style>
