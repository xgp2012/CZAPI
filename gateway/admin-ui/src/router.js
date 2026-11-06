import { createRouter, createWebHistory } from 'vue-router'
import http from './api'

const routes = [
  { path: '/login', name: 'login', component: () => import('./views/Login.vue'), meta: { public: true } },
  { path: '/password', name: 'password', component: () => import('./views/ChangePassword.vue') },
  {
    path: '/',
    component: () => import('./views/Layout.vue'),
    children: [
      { path: '', redirect: '/overview' },
      { path: 'overview', name: 'overview', component: () => import('./views/Overview.vue'), meta: { title: '概览' } },
      { path: 'sites', name: 'sites', component: () => import('./views/Sites.vue'), meta: { title: '站点管理' } },
      { path: 'accounts', name: 'accounts', component: () => import('./views/Accounts.vue'), meta: { title: '账户与额度' } },
      { path: 'ledger', name: 'ledger', component: () => import('./views/Ledger.vue'), meta: { title: '账本对账' } },
      { path: 'keys', name: 'keys', component: () => import('./views/Keys.vue'), meta: { title: 'Key 管理' } },
      { path: 'hosts', name: 'hosts', component: () => import('./views/Hosts.vue'), meta: { title: 'Host 与订阅' } },
      { path: 'usage', name: 'usage', component: () => import('./views/Usage.vue'), meta: { title: '用量报表' } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/overview' },
]

const router = createRouter({
  // history 模式需要后端把 /admin/* 都回落到 index.html
  history: createWebHistory('/admin/'),
  routes,
})

// 路由守卫：每次跳转先问后端"我是谁"，不在前端缓存登录态。
// 缓存会导致"后端已锁死账号，前端还以为登录着"的危险状态。
router.beforeEach(async (to) => {
  if (to.meta.public) return true

  try {
    const me = await http.get('/me')
    if (me.must_change_password && to.name !== 'password') {
      return { name: 'password' }
    }
    return true
  } catch {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
})

export default router
