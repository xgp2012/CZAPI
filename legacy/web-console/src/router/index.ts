import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { getStoredToken } from '@/api/client'

/**
 * Single SPA, two surfaces.
 * ----------------------------------------------------------------------------
 * The Go gateway embeds the build and serves it under `/console/`, so the
 * router base is `/console/`. That means the admin surface physically lives at
 * `/console/admin/...` in the address bar while remaining logically `/admin/...`
 * inside this app: every route below is declared with its logical path and the
 * base is applied by the history instance. Links therefore keep working with
 * both `router.push('/admin/channels')` and a direct visit to
 * `https://host/console/admin/channels`.
 *
 * The layout switch is driven purely by the resolved path prefix (see App.vue).
 */

const ConsoleOverview = () => import('@/views/console/OverviewView.vue')
const ConsoleKeys = () => import('@/views/console/KeysView.vue')
const ConsoleModels = () => import('@/views/console/ModelsView.vue')
const ConsoleUsage = () => import('@/views/console/UsageView.vue')
const ConsoleDocs = () => import('@/views/console/DocsView.vue')

const AdminLogin = () => import('@/views/admin/LoginView.vue')
const AdminDashboard = () => import('@/views/admin/DashboardView.vue')
const AdminChannels = () => import('@/views/admin/ChannelsView.vue')
const AdminModels = () => import('@/views/admin/ModelsAdminView.vue')
const AdminPlans = () => import('@/views/admin/PlansView.vue')
const AdminAccounts = () => import('@/views/admin/AccountsView.vue')
const AdminSubscriptions = () => import('@/views/admin/SubscriptionsView.vue')
const AdminLedger = () => import('@/views/admin/LedgerView.vue')
const AdminReconcile = () => import('@/views/admin/ReconcileView.vue')
const AdminFlags = () => import('@/views/admin/FlagsView.vue')
const AdminAudit = () => import('@/views/admin/AuditView.vue')
const AdminSystem = () => import('@/views/admin/SystemView.vue')

const routes: RouteRecordRaw[] = [
  // ---------------- user centre ----------------
  { path: '/', name: 'console-overview', component: ConsoleOverview, meta: { title: '概览', surface: 'console' } },
  { path: '/keys', name: 'console-keys', component: ConsoleKeys, meta: { title: 'Key 管理', surface: 'console' } },
  { path: '/models', name: 'console-models', component: ConsoleModels, meta: { title: '模型列表', surface: 'console' } },
  { path: '/usage', name: 'console-usage', component: ConsoleUsage, meta: { title: '用量明细', surface: 'console' } },
  { path: '/docs', name: 'console-docs', component: ConsoleDocs, meta: { title: '接入文档', surface: 'console' } },

  // ---------------- admin ----------------
  { path: '/admin/login', name: 'admin-login', component: AdminLogin, meta: { title: '管理员登录', surface: 'admin', public: true } },
  { path: '/admin', name: 'admin-dashboard', component: AdminDashboard, meta: { title: '仪表盘', surface: 'admin' } },
  { path: '/admin/channels', name: 'admin-channels', component: AdminChannels, meta: { title: '渠道管理', surface: 'admin' } },
  { path: '/admin/models', name: 'admin-models', component: AdminModels, meta: { title: '模型与定价', surface: 'admin' } },
  { path: '/admin/plans', name: 'admin-plans', component: AdminPlans, meta: { title: '套餐', surface: 'admin' } },
  { path: '/admin/accounts', name: 'admin-accounts', component: AdminAccounts, meta: { title: '账户与 Key', surface: 'admin' } },
  { path: '/admin/subscriptions', name: 'admin-subscriptions', component: AdminSubscriptions, meta: { title: '订阅', surface: 'admin' } },
  { path: '/admin/ledger', name: 'admin-ledger', component: AdminLedger, meta: { title: '财务流水', surface: 'admin' } },
  { path: '/admin/reconcile', name: 'admin-reconcile', component: AdminReconcile, meta: { title: '对账', surface: 'admin' } },
  { path: '/admin/flags', name: 'admin-flags', component: AdminFlags, meta: { title: '功能开关', surface: 'admin' } },
  { path: '/admin/audit', name: 'admin-audit', component: AdminAudit, meta: { title: '审计日志', surface: 'admin' } },
  { path: '/admin/system', name: 'admin-system', component: AdminSystem, meta: { title: '系统设置', surface: 'admin' } },

  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory('/console/'),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

/**
 * Admin routes require a token. In mock mode this is still reachable: the login
 * screen offers a one-click demo identity, so a reviewer with no backend is never
 * blocked — they are only asked to acknowledge which surface they are entering.
 */
router.beforeEach((to) => {
  if (to.meta.surface !== 'admin' || to.meta.public) return true
  if (getStoredToken()) return true
  return { name: 'admin-login', query: { redirect: to.fullPath } }
})

router.afterEach((to) => {
  const title = (to.meta.title as string | undefined) ?? '控制台'
  document.title = `${title} · CZAPI`
})
