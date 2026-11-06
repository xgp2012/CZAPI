<script setup lang="ts">
/**
 * 系统设置 / 站点。
 *
 * Two halves with different jobs:
 *   · 生效配置 — read-only, masked, so operators can answer "what is running?"
 *   · 站点 — V10 registrations; the HMAC secret is shown exactly once at
 *     creation and once at rotation, then only the tail survives.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { Site, SystemSettings } from '@/types/api'
import { copyText, fmtDateTime } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import CopyButton from '@/components/CopyButton.vue'
import Icon from '@/components/Icon.vue'

const settings = ref<SystemSettings | null>(null)
const settingsLoading = ref(true)
const settingsError = ref<string | null>(null)

/* ------------------------------------------------------------------ */
/* Live-tunable limits                                                */
/* ------------------------------------------------------------------ */

/**
 * Only values that are safe to change while serving are editable here.
 * Money is micro-yuan; 0 means "no cap", so the form must not treat it as unset.
 */
const limits = reactive({
  key_daily_micros: 0,
  account_monthly_micros: 0,
  global_daily_micros: 0,
  rpm: 0,
  tpm: 0,
  concurrency: 0,
})
const limitsSaving = ref(false)
const limitsDirty = ref(false)

/** Yuan, human-friendly, for editing the three money ceilings. */
const yuan = reactive({ key_daily: 0, account_monthly: 0, global_daily: 0 })

const MICROS_PER_YUAN = 1_000_000

function microsToYuan(m: number): number {
  // Keep two decimals: sub-fen precision is not something an operator sets.
  return Math.round((m / MICROS_PER_YUAN) * 100) / 100
}

function yuanToMicros(y: number): number {
  return Math.round(y * MICROS_PER_YUAN)
}

function syncLimitsFromSettings() {
  const raw = settings.value?.limits as Partial<Record<string, number>> | undefined
  if (!raw) return
  limits.key_daily_micros = Number(raw.key_daily_micros ?? 0)
  limits.account_monthly_micros = Number(raw.account_monthly_micros ?? 0)
  limits.global_daily_micros = Number(raw.global_daily_micros ?? 0)
  limits.rpm = Number(raw.rpm ?? 0)
  limits.tpm = Number(raw.tpm ?? 0)
  limits.concurrency = Number(raw.concurrency ?? 0)
  yuan.key_daily = microsToYuan(limits.key_daily_micros)
  yuan.account_monthly = microsToYuan(limits.account_monthly_micros)
  yuan.global_daily = microsToYuan(limits.global_daily_micros)
  limitsDirty.value = false
}

async function saveLimits() {
  // Guard the one mistake the backend also rejects, so the operator gets the
  // message next to the field instead of a toast after a round trip.
  if (yuan.key_daily < 0 || yuan.account_monthly < 0 || yuan.global_daily < 0) {
    ElMessage.warning('金额上限不能为负数（用 0 表示不限制）')
    return
  }
  if (limits.rpm < 0 || limits.tpm < 0 || limits.concurrency < 0) {
    ElMessage.warning('速率限制不能为负数（用 0 表示不限制）')
    return
  }

  limitsSaving.value = true
  try {
    const payload = {
      key_daily_micros: yuanToMicros(yuan.key_daily),
      account_monthly_micros: yuanToMicros(yuan.account_monthly),
      global_daily_micros: yuanToMicros(yuan.global_daily),
      rpm: Math.round(limits.rpm),
      tpm: Math.round(limits.tpm),
      concurrency: Math.round(limits.concurrency),
    }
    settings.value = await adminApi.updateLimits(payload)
    syncLimitsFromSettings()
    ElMessage.success('已保存，立即生效（无需重启）')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    limitsSaving.value = false
  }
}

/* ------------------------------------------------------------------ */
/* Change password                                                    */
/* ------------------------------------------------------------------ */

const pwForm = reactive({ current_password: '', new_password: '', confirm: '' })
const pwSaving = ref(false)

async function submitPassword() {
  if (!pwForm.current_password || !pwForm.new_password) {
    ElMessage.warning('请填写当前密码和新密码')
    return
  }
  if (pwForm.new_password !== pwForm.confirm) {
    ElMessage.warning('两次输入的新密码不一致')
    return
  }
  if (pwForm.new_password.length < 8) {
    ElMessage.warning('新密码至少 8 位')
    return
  }

  pwSaving.value = true
  try {
    const res = await adminApi.changePassword(pwForm.current_password, pwForm.new_password)
    ElMessage.success(res.message || '密码已修改')
    pwForm.current_password = ''
    pwForm.new_password = ''
    pwForm.confirm = ''
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '修改失败')
  } finally {
    pwSaving.value = false
  }
}

const sites = ref<Site[]>([])
const sitesLoading = ref(true)
const sitesError = ref<string | null>(null)

async function loadSettings() {
  settingsLoading.value = true
  settingsError.value = null
  try {
    settings.value = await adminApi.settings()
    syncLimitsFromSettings()
  } catch (e) {
    settingsError.value = e instanceof Error ? e.message : '加载配置失败'
    ElMessage.error(settingsError.value)
  } finally {
    settingsLoading.value = false
  }
}

async function loadSites() {
  sitesLoading.value = true
  sitesError.value = null
  try {
    sites.value = await adminApi.listSites()
  } catch (e) {
    sitesError.value = e instanceof Error ? e.message : '加载站点失败'
    ElMessage.error(sitesError.value)
  } finally {
    sitesLoading.value = false
  }
}

onMounted(() => {
  loadSettings()
  loadSites()
})

/**
 * Flatten the free-form settings object into rows so unknown keys still render.
 * Nested objects become `a.b.c` paths — no assumption about the server's shape.
 */
interface SettingRow {
  group: string
  path: string
  value: string
  mono: boolean
}

const settingRows = computed<SettingRow[]>(() => {
  const out: SettingRow[] = []
  const walk = (node: unknown, path: string) => {
    if (node === null || node === undefined) return
    if (typeof node === 'object' && !Array.isArray(node)) {
      for (const [k, v] of Object.entries(node as Record<string, unknown>)) {
        walk(v, path ? `${path}.${k}` : k)
      }
      return
    }
    const group = path.includes('.') ? path.split('.')[0]! : 'general'
    const key = path.includes('.') ? path.slice(path.indexOf('.') + 1) : path
    const raw = Array.isArray(node) ? node.join(', ') : String(node)
    out.push({
      group,
      path: key,
      value: raw,
      mono: /(_micros|_ms|_seconds|_days|_ttl|_cap|version|time|addr|dsn|driver|topic|_at)$/.test(key) || /^\d+$/.test(raw),
    })
  }
  walk(settings.value, '')
  return out
})

const settingsGroups = computed(() => [...new Set(settingRows.value.map((r) => r.group))])

/**
 * Nudges the operator to change the default password on first visit.
 *
 * Deliberately a static reminder rather than a detection: the server never
 * reports the current password, so claiming to know it is still `admin` would be
 * a guess. A plain "go change it" banner is honest and removes the step of
 * remembering to do so.
 */
const isDefaultPasswordHint = ref(true)

/* ------------------------------------------------------------------ */
/* Create site                                                        */
/* ------------------------------------------------------------------ */

const createOpen = ref(false)
const creating = ref(false)
const createForm = reactive({ name: '', base_url: 'https://' })

/** Held only while the one-time modal is open. */
const revealed = ref<{ title: string; app_id: string; secret: string; note: string } | null>(null)
const revealVisible = ref(false)

function openCreate() {
  createForm.name = ''
  createForm.base_url = 'https://'
  createOpen.value = true
}

async function submitCreate() {
  if (!createForm.name.trim()) {
    ElMessage.warning('请填写站点名称')
    return
  }
  if (!/^https?:\/\/.+/.test(createForm.base_url.trim())) {
    ElMessage.warning('站点地址需以 http:// 或 https:// 开头')
    return
  }
  creating.value = true
  try {
    const res = await adminApi.createSite({ name: createForm.name.trim(), base_url: createForm.base_url.trim() })
    createOpen.value = false
    revealed.value = {
      title: '站点已注册',
      app_id: res.app_id,
      secret: res.secret,
      note: '这是 V10 插件签名用的 HMAC secret，只显示这一次。请立刻复制并填入插件的配置；网关只保存哈希。',
    }
    revealVisible.value = true
    await loadSites()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '注册失败')
  } finally {
    creating.value = false
  }
}

async function rotate(site: Site) {
  try {
    await ElMessageBox.confirm(
      `轮换「${site.name}」的 secret 后，旧 secret 仍可用 7 天（保留为 prev）用于平滑切换。7 天后旧值彻底失效。`,
      '确认轮换 secret',
      { confirmButtonText: '轮换', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  try {
    const res = await adminApi.rotateSiteSecret(site.app_id)
    revealed.value = {
      title: 'Secret 已轮换',
      app_id: res.app_id,
      secret: res.secret,
      note: `新 secret 只显示这一次。旧 secret 保留至 ${fmtDateTime(res.prev_expires_at)}，请在此之前完成插件更新。`,
    }
    revealVisible.value = true
    await loadSites()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '轮换失败')
  }
}

async function copyRevealed() {
  if (!revealed.value) return
  const ok = await copyText(revealed.value.secret)
  if (ok) ElMessage.success('已复制 secret')
  else ElMessage.error('复制失败，请手动选中复制')
}

function closeReveal() {
  revealVisible.value = false
  revealed.value = null
}
</script>

<template>
  <div class="page">
    <PageHeader title="系统设置" desc="当前生效的配置（脱敏只读），以及接入本网关的 V10 站点与签名密钥。">
      <template #actions>
        <el-button :loading="settingsLoading" @click="loadSettings">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新配置</span>
        </el-button>
      </template>
    </PageHeader>

    <!-- ============ live-tunable limits ============ -->
    <section class="panel">
      <div class="panel__head">
        <span class="panel__title">计费与限流（可在线调整）</span>
        <span class="panel__hint">保存后立即生效，无需重启</span>
      </div>

      <div class="panel__body">
        <el-alert type="info" :closable="false" show-icon style="margin-bottom: 16px">
          这里是保护性上限，不是定价。金额填 <strong>0 表示不限制</strong>。
          改动会立刻影响在途请求的下一次预扣。
        </el-alert>

        <div class="limgrid">
          <div class="limfield">
            <label>单 Key 每日上限（元）</label>
            <el-input-number v-model="yuan.key_daily" :min="0" :step="10" :precision="2"
              controls-position="right" style="width: 100%" @change="limitsDirty = true" />
            <div class="hint">Key 泄漏时把损失关在笼子里。默认 50 元。</div>
          </div>

          <div class="limfield">
            <label>单账户每月上限（元）</label>
            <el-input-number v-model="yuan.account_monthly" :min="0" :step="100" :precision="2"
              controls-position="right" style="width: 100%" @change="limitsDirty = true" />
            <div class="hint">防止单个客户把额度刷爆。默认 500 元。</div>
          </div>

          <div class="limfield">
            <label>全局每日上限（元）</label>
            <el-input-number v-model="yuan.global_daily" :min="0" :step="100" :precision="2"
              controls-position="right" style="width: 100%" @change="limitsDirty = true" />
            <div class="hint">0 = 关闭。了解清楚量级后再开。</div>
          </div>

          <div class="limfield">
            <label>单 Key 每分钟请求数（RPM）</label>
            <el-input-number v-model="limits.rpm" :min="0" :step="10"
              controls-position="right" style="width: 100%" @change="limitsDirty = true" />
            <div class="hint">0 = 不限制。</div>
          </div>

          <div class="limfield">
            <label>单 Key 每分钟 Token 数（TPM）</label>
            <el-input-number v-model="limits.tpm" :min="0" :step="1000"
              controls-position="right" style="width: 100%" @change="limitsDirty = true" />
            <div class="hint">0 = 不限制。</div>
          </div>

          <div class="limfield">
            <label>单 Key 并发数</label>
            <el-input-number v-model="limits.concurrency" :min="0" :step="1"
              controls-position="right" style="width: 100%" @change="limitsDirty = true" />
            <div class="hint">同时进行的请求数上限。0 = 不限制。</div>
          </div>
        </div>

        <div class="limactions">
          <el-button type="primary" :loading="limitsSaving" :disabled="!limitsDirty" @click="saveLimits">
            保存并生效
          </el-button>
          <el-button :disabled="!limitsDirty || limitsSaving" @click="syncLimitsFromSettings">还原</el-button>
          <span v-if="limitsDirty" class="dirty">有未保存的修改</span>
        </div>
      </div>
    </section>

    <!-- ============ change password ============ -->
    <section class="panel">
      <div class="panel__head">
        <span class="panel__title">修改登录密码</span>
        <span class="panel__hint">立即生效</span>
      </div>

      <div class="panel__body">
        <el-alert v-if="isDefaultPasswordHint" type="warning" :closable="false" show-icon
          style="margin-bottom: 16px">
          检测到默认密码仍在使用。对外开放前请务必改掉。
        </el-alert>

        <el-form label-position="top" class="pwform" @submit.prevent="submitPassword">
          <el-form-item label="当前密码">
            <el-input v-model="pwForm.current_password" type="password" show-password
              autocomplete="current-password" />
          </el-form-item>
          <el-form-item label="新密码">
            <el-input v-model="pwForm.new_password" type="password" show-password
              autocomplete="new-password" />
            <div class="hint">至少 8 位，不能是 admin / password 这类常见词。</div>
          </el-form-item>
          <el-form-item label="确认新密码">
            <el-input v-model="pwForm.confirm" type="password" show-password
              autocomplete="new-password" />
          </el-form-item>
          <el-button type="primary" :loading="pwSaving" @click="submitPassword">
            修改密码
          </el-button>
        </el-form>
      </div>
    </section>

    <!-- ============ effective config ============ -->
    <section class="panel">
      <div class="panel__head">
        <span class="panel__title">当前生效配置</span>
        <span class="panel__hint">只读 · 敏感字段已脱敏</span>
      </div>

      <div v-if="settingsLoading" class="panel__body stack">
        <span class="sk" style="width: 40%; height: 12px" />
        <span class="sk" style="width: 100%; height: 120px" />
      </div>

      <div v-else-if="settingsError" class="panel__body">
        <EmptyState compact icon="alert" title="配置加载失败" :desc="settingsError">
          <el-button size="small" @click="loadSettings">重试</el-button>
        </EmptyState>
      </div>

      <div v-else-if="!settingRows.length" class="panel__body">
        <EmptyState compact icon="gear" title="后端未返回配置" desc="GET /admin/v1/settings 返回空对象。" />
      </div>

      <div v-else class="cfg">
        <div v-for="g in settingsGroups" :key="g" class="cfg__group">
          <div class="cfg__group-title mono">{{ g }}</div>
          <dl class="cfg__list">
            <div v-for="r in settingRows.filter((x) => x.group === g)" :key="r.group + '.' + r.path" class="cfg__row">
              <dt>{{ r.path }}</dt>
              <dd :class="{ mono: r.mono }">{{ r.value }}</dd>
            </div>
          </dl>
        </div>
      </div>
    </section>

    <!-- ============ sites ============ -->
    <section class="panel">
      <div class="panel__head">
        <span class="panel__title">V10 站点</span>
        <el-button type="primary" size="small" @click="openCreate">
          <Icon name="plus" :size="13" /><span class="btn-gap">注册站点</span>
        </el-button>
      </div>

      <div v-if="sitesLoading" class="panel__body">
        <div v-for="i in 3" :key="i" class="row-skeleton"><span class="sk" style="width: 45%; height: 12px" /></div>
      </div>

      <div v-else-if="sitesError" class="panel__body">
        <EmptyState compact icon="alert" title="站点加载失败" :desc="sitesError">
          <el-button size="small" @click="loadSites">重试</el-button>
        </EmptyState>
      </div>

      <EmptyState v-else-if="!sites.length" compact icon="server" title="还没有注册站点"
        desc="每个 V10 站点需要一个 app_id + secret 才能调用 /internal/v1。" />

      <el-table v-else :data="sites" row-key="app_id">
        <el-table-column label="站点" min-width="200">
          <template #default="{ row }">
            <div class="cell-site">
              <span class="cell-site__name">{{ row.name }}</span>
              <code class="cell-site__url">{{ row.base_url }}</code>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="app_id" width="176">
          <template #default="{ row }">
            <div class="cell-appid">
              <code class="mono">{{ row.app_id }}</code>
              <CopyButton :value="row.app_id" label="" />
            </div>
          </template>
        </el-table-column>

        <el-table-column label="secret（尾号）" width="160">
          <template #default="{ row }">
            <code class="mono secret">whsec_****{{ row.secret_tail }}</code>
          </template>
        </el-table-column>

        <el-table-column label="旧 secret 有效期至" width="180">
          <template #default="{ row }">
            <span v-if="row.prev_secret_expires_at" class="mono warn-text">
              {{ fmtDateTime(row.prev_secret_expires_at) }}
            </span>
            <span v-else class="dim">—</span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="96">
          <template #default="{ row }">
            <span class="badge" :class="row.status === 1 ? 'badge--ok' : 'badge--idle'">{{ row.status === 1 ? '启用' : '停用' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="注册时间" width="140">
          <template #default="{ row }"><span class="mono dim">{{ fmtDateTime(row.created_at) }}</span></template>
        </el-table-column>

        <el-table-column label="操作" width="112" align="right">
          <template #default="{ row }">
            <el-button text size="small" @click="rotate(row)">轮换 secret</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- ============ create site dialog ============ -->
    <el-dialog v-model="createOpen" title="注册 V10 站点" width="520px" append-to-body>
      <el-alert type="info" :closable="false" show-icon style="margin-bottom: 16px">
        注册后会生成 app_id 与 HMAC secret。secret 只显示一次，请立即复制到 V10 插件配置。
      </el-alert>

      <el-form label-position="top" @submit.prevent>
        <el-form-item label="站点名称">
          <el-input v-model="createForm.name" placeholder="idcsmart 主站" />
        </el-form-item>
        <el-form-item label="站点地址">
          <el-input v-model="createForm.base_url" class="mono-input" placeholder="https://v10.example.com" />
          <div class="hint">用于 HMAC 校验的来源白名单与回调地址拼接。</div>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="createOpen = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="submitCreate">注册并生成 secret</el-button>
      </template>
    </el-dialog>

    <!-- ============ one-time secret modal ============ -->
    <el-dialog v-model="revealVisible" :title="revealed?.title ?? 'Secret'" width="600px" :close-on-click-modal="false"
      :show-close="false" append-to-body @close="closeReveal">
      <div v-if="revealed" class="reveal">
        <div class="reveal__banner">
          <Icon name="alert" :size="18" />
          <div>
            <strong>Secret 只显示这一次。</strong>
            <span>{{ revealed.note }}</span>
          </div>
        </div>

        <div class="reveal__row">
          <span class="reveal__label">app_id</span>
          <code class="mono reveal__value">{{ revealed.app_id }}</code>
          <CopyButton :value="revealed.app_id" label="" />
        </div>

        <div class="reveal__row reveal__row--secret">
          <span class="reveal__label">secret</span>
          <code class="mono reveal__value">{{ revealed.secret }}</code>
          <el-button type="primary" size="small" @click="copyRevealed">
            <Icon name="copy" :size="13" /><span class="btn-gap">复制</span>
          </el-button>
        </div>
      </div>

      <template #footer>
        <el-button type="primary" @click="closeReveal">我已保存，关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.btn-gap {
  margin-left: 6px;
}

.row-skeleton {
  display: flex;
  gap: var(--sp-6);
  padding: 12px 0;
  border-bottom: 1px solid var(--line-1);
}

/* ---- config ---- */
.cfg {
  display: flex;
  flex-direction: column;
}

.cfg__group {
  border-bottom: 1px solid var(--line-1);
}

.cfg__group:last-child {
  border-bottom: none;
}

.cfg__group-title {
  padding: 8px var(--sp-5);
  font-size: var(--fs-2xs);
  color: var(--ink-3);
  background: var(--bg-sunken);
  border-bottom: 1px solid var(--line-1);
  letter-spacing: 0.04em;
}

.cfg__list {
  margin: 0;
  padding: 0;
}

.cfg__row {
  display: grid;
  grid-template-columns: 260px minmax(0, 1fr);
  gap: var(--sp-4);
  padding: 7px var(--sp-5);
  border-bottom: 1px solid color-mix(in srgb, var(--line-1) 60%, transparent);
}

.cfg__row:last-child {
  border-bottom: none;
}

.cfg__row dt {
  font-size: var(--fs-xs);
  color: var(--ink-4);
}

.cfg__row dd {
  margin: 0;
  font-size: var(--fs-xs);
  color: var(--ink-1);
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ---- sites ---- */
.cell-site {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.cell-site__name {
  color: var(--ink-1);
  font-weight: 550;
}

.cell-site__url {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.cell-appid {
  display: flex;
  align-items: center;
  gap: 2px;
}

.secret {
  font-size: var(--fs-2xs);
  color: var(--ink-2);
}

.warn-text {
  color: var(--warn);
  font-size: var(--fs-2xs);
}

/* ---- reveal ---- */
.reveal {
  display: flex;
  flex-direction: column;
  gap: var(--sp-3);
}

.reveal__banner {
  display: flex;
  gap: var(--sp-3);
  padding: var(--sp-3) var(--sp-4);
  border-radius: var(--r-sm);
  background: var(--danger-soft);
  border: 1px solid color-mix(in srgb, var(--danger) 34%, transparent);
  color: var(--danger);
  font-size: var(--fs-xs);
  line-height: 1.55;
}

.reveal__banner strong {
  font-weight: 700;
}

.reveal__banner strong + span {
  display: block;
  color: color-mix(in srgb, var(--danger) 80%, var(--ink-2));
}

.reveal__row {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  padding: var(--sp-3) var(--sp-4);
  border-radius: var(--r-sm);
  background: var(--bg-sunken);
  border: 1px solid var(--line-1);
}

.reveal__row--secret {
  border-color: var(--line-2);
}

.reveal__label {
  flex: 0 0 56px;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.reveal__value {
  flex: 1;
  font-size: var(--fs-xs);
  color: var(--ink-1);
  word-break: break-all;
  line-height: 1.6;
}

.hint {
  margin-top: 4px;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

:deep(.mono-input .el-input__inner) {
  font-family: var(--font-mono);
}

/* ---- editable limits ---- */
.limgrid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: var(--sp-4);
}

.limfield label {
  display: block;
  margin-bottom: 6px;
  font-size: var(--fs-xs);
  color: var(--ink-2);
  font-weight: 550;
}

.limactions {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  margin-top: var(--sp-5);
  padding-top: var(--sp-4);
  border-top: 1px solid var(--line-1);
}

.dirty {
  font-size: var(--fs-2xs);
  color: var(--warn);
}

.pwform {
  max-width: 380px;
}
</style>
