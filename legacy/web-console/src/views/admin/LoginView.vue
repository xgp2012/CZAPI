<script setup lang="ts">
/**
 * Admin login.
 *
 * Split layout rather than a centred card: the left half states what this
 * console controls (money and keys) so an operator knows they are in the right
 * place, and it doubles as the mock-mode explainer when no backend exists.
 */
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '@/stores/auth'
import { isMock } from '@/api/client'
import Icon from '@/components/Icon.vue'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

const form = reactive({ username: 'admin', password: '' })
const error = ref<string | null>(null)
const mock = computed(() => isMock())

/** Where to go after login: the originally requested admin page, if any. */
const target = computed(() => {
  const r = route.query.redirect
  return typeof r === 'string' && r.startsWith('/admin') ? r : '/admin'
})

async function submit() {
  error.value = null
  if (!form.username.trim() || !form.password) {
    error.value = '请输入用户名和密码'
    return
  }
  try {
    await auth.login(form.username.trim(), form.password)
    ElMessage.success('登录成功')
    router.push(target.value)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '登录失败'
  }
}

function quickIn() {
  auth.demoSignIn()
  router.push(target.value)
}
</script>

<template>
  <div class="login">
    <!-- left: what this place is -->
    <section class="login__aside">
      <div class="login__brand">
        <span class="login__mark">
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.1"
            stroke-linecap="round" stroke-linejoin="round">
            <path d="M4 17.5 9.5 6l4 7.5L17 9l3 8.5" />
          </svg>
        </span>
        <span class="login__word">CZAPI</span>
      </div>

      <div class="login__pitch">
        <h1>网关的钱、Key 与渠道，<br />都在这一块面板上。</h1>
        <p>
          这里管理上游渠道与模型定价、账户余额与订阅额度、幂等的财务流水，
          以及每天的对账差异。收款与卡密兑换始终在 V10 侧完成，网关只记账。
        </p>
      </div>

      <ul class="login__facts">
        <li><span class="mono">1</span> 金额一律以微元（1 元 = 1,000,000）入库，页面按元展示</li>
        <li><span class="mono">2</span> Key 只保存哈希，明文仅在创建时出现一次</li>
        <li><span class="mono">3</span> 每个加额凭证（<code class="mono">ref_type + ref_id</code>）只能入账一次</li>
      </ul>

      <p class="login__foot">CZAPI LLM Gateway · console-api-v1</p>
    </section>

    <!-- right: the form -->
    <section class="login__panel">
      <div class="login__form">
        <h2>管理员登录</h2>
        <p class="login__sub">使用网关管理员账号登录。会话 12 小时后过期。</p>

        <div v-if="mock" class="login__mock">
          <Icon name="info" :size="15" />
          <p>
            当前是 <b>mock 模式</b>（未连接后端）。任意用户名 + 至少 4 位密码即可登录，
            或直接点击下方的「以演示身份进入」。
          </p>
        </div>

        <el-form label-position="top" @submit.prevent="submit">
          <el-form-item label="用户名">
            <el-input v-model="form.username" placeholder="admin" size="large" autocomplete="username" />
          </el-form-item>
          <el-form-item label="密码">
            <el-input v-model="form.password" type="password" placeholder="••••••••" size="large" show-password
              autocomplete="current-password" @keyup.enter="submit" />
          </el-form-item>

          <p v-if="error" class="login__error">{{ error }}</p>

          <el-button type="primary" size="large" style="width: 100%" :loading="auth.loading" @click="submit">
            登录
          </el-button>

          <el-button v-if="mock" size="large" style="width: 100%; margin: 10px 0 0" @click="quickIn">
            以演示身份进入
          </el-button>
        </el-form>

        <div class="login__links">
          <RouterLink to="/">← 返回用户中心</RouterLink>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.login {
  display: grid;
  grid-template-columns: minmax(0, 1.05fr) minmax(0, 1fr);
  min-height: 100vh;
  background: var(--bg-canvas);
}

/* ---- aside ---- */
.login__aside {
  display: flex;
  flex-direction: column;
  gap: var(--sp-8);
  padding: var(--sp-10) var(--sp-12);
  background: var(--rail-bg);
  color: var(--rail-ink);
  position: relative;
  overflow: hidden;
}

.login__aside::after {
  /* a single faint diagonal line set: the "signal" motif, quiet */
  content: '';
  position: absolute;
  inset: auto -20% -30% auto;
  width: 620px;
  height: 620px;
  background: repeating-linear-gradient(
    115deg,
    rgba(255, 178, 91, 0.055) 0px,
    rgba(255, 178, 91, 0.055) 1px,
    transparent 1px,
    transparent 13px
  );
  pointer-events: none;
}

.login__brand {
  display: flex;
  align-items: center;
  gap: var(--sp-2);
}

.login__mark {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  border-radius: 7px;
  color: var(--accent);
  background: rgba(255, 178, 91, 0.12);
  border: 1px solid rgba(255, 178, 91, 0.22);
}

.login__word {
  font-size: var(--fs-md);
  font-weight: 640;
  letter-spacing: 0.02em;
  color: var(--rail-ink-strong);
}

.login__pitch {
  display: flex;
  flex-direction: column;
  gap: var(--sp-4);
  max-width: 34ch;
  position: relative;
  z-index: 1;
}

.login__pitch h1 {
  font-size: var(--fs-2xl);
  line-height: 1.35;
  font-weight: 620;
  color: var(--rail-ink-strong);
  letter-spacing: -0.01em;
}

.login__pitch p {
  font-size: var(--fs-sm);
  line-height: 1.75;
  color: var(--rail-ink);
}

.login__facts {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--sp-3);
  max-width: 46ch;
  position: relative;
  z-index: 1;
}

.login__facts li {
  display: flex;
  gap: var(--sp-3);
  font-size: var(--fs-xs);
  line-height: 1.65;
  color: var(--rail-ink);
}

.login__facts .mono {
  flex: 0 0 18px;
  height: 18px;
  display: grid;
  place-items: center;
  border-radius: 3px;
  border: 1px solid rgba(255, 178, 91, 0.24);
  color: var(--accent);
  font-size: 10px;
}

.login__foot {
  margin-top: auto;
  font-size: var(--fs-2xs);
  color: rgba(147, 167, 184, 0.6);
  letter-spacing: 0.04em;
}

/* ---- panel ---- */
.login__panel {
  display: grid;
  place-items: center;
  padding: var(--sp-10);
  background: var(--bg-canvas);
}

.login__form {
  width: min(380px, 100%);
}

.login__form h2 {
  font-size: var(--fs-lg);
  font-weight: 620;
  margin-bottom: 4px;
}

.login__sub {
  font-size: var(--fs-xs);
  color: var(--ink-3);
  margin-bottom: var(--sp-6);
}

.login__mock {
  display: flex;
  gap: var(--sp-3);
  padding: var(--sp-3) var(--sp-4);
  margin-bottom: var(--sp-5);
  border-radius: var(--r-sm);
  background: var(--info-soft);
  border: 1px solid color-mix(in srgb, var(--info) 24%, transparent);
  color: var(--info);
}

.login__mock p {
  font-size: var(--fs-xs);
  line-height: 1.6;
  color: var(--ink-2);
}

.login__error {
  margin: -4px 0 var(--sp-3);
  font-size: var(--fs-xs);
  color: var(--danger);
}

.login__links {
  margin-top: var(--sp-5);
  font-size: var(--fs-xs);
  color: var(--ink-4);
}

@media (max-width: 900px) {
  .login {
    grid-template-columns: 1fr;
  }
  .login__aside {
    display: none;
  }
}
</style>
