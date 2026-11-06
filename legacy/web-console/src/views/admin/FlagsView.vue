<script setup lang="ts">
/**
 * 功能开关 — four flags, global value plus optional per-account overrides.
 *
 * The layout separates the *global* switch (big, load-bearing) from the
 * *override* list (small, exceptional) because flipping billing_enforced off
 * globally is a company-level decision, while an override for one test tenant
 * is routine. Each change is immediate and hot-reloaded by the gateway.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { FlagKey, FlagValue } from '@/types/api'
import { fmtDateTime } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

interface FlagMeta {
  key: FlagKey
  label: string
  desc: string
  /** What happens when it is OFF — the part operators must know. */
  offEffect: string
  risk: 'low' | 'high'
}

const FLAGS: FlagMeta[] = [
  {
    key: 'gateway_enabled',
    label: '网关总开关',
    desc: '所有 /v1/* 请求的总闸门。健康检查与 /internal/v1/health 不受影响。',
    offEffect: '关闭后所有推理请求返回 503 feature_disabled，客户侧立即中断。',
    risk: 'high',
  },
  {
    key: 'billing_enforced',
    label: '强制扣费',
    desc: '关闭时照常计量与落库，但不扣任何额度，记账为 billing_mode=PaidByFree。',
    offEffect: '关闭后所有调用免费——只用于上游故障赔付窗口。',
    risk: 'high',
  },
  {
    key: 'module_autocreate_enabled',
    label: '下单自动开通',
    desc: 'V10 下单后自动创建账户与订阅，无需人工介入。',
    offEffect: '关闭后新订单不会自动开通，需要运维手工创建订阅。',
    risk: 'low',
  },
  {
    key: 'recharge_enabled',
    label: '充值入口',
    desc: '是否在用户中心显示可用的充值跳转。收款始终在 V10 完成。',
    offEffect: '关闭后用户中心隐藏充值入口（余额耗尽即中断）。',
    risk: 'low',
  },
]

const flags = ref<FlagValue[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const busy = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    flags.value = await adminApi.listFlags()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载开关失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const globalOf = (key: string) => flags.value.find((f) => f.key === key && !f.scope)
const overridesOf = (key: string) => flags.value.filter((f) => f.key === key && f.scope)

const enabledCount = computed(() => FLAGS.filter((f) => globalOf(f.key)?.value).length)

/* ------------------------------------------------------------------ */
/* Toggle global                                                      */
/* ------------------------------------------------------------------ */

async function toggleGlobal(meta: FlagMeta, next: boolean) {
  const id = `global:${meta.key}`
  busy.value = id
  try {
    await adminApi.setFlag(meta.key, next, '')
    ElMessage.success(`${meta.label} 已${next ? '开启' : '关闭'}（热生效，无需重启）`)
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    busy.value = null
  }
}

/* ------------------------------------------------------------------ */
/* Overrides                                                          */
/* ------------------------------------------------------------------ */

const overrideForm = reactive({
  key: 'billing_enforced' as FlagKey,
  scope: '',
  value: false,
})

async function addOverride() {
  const scope = overrideForm.scope.trim()
  if (!/^\d+$/.test(scope)) {
    ElMessage.warning('scope 必须是数字账户 ID（留空表示全局）')
    return
  }
  const exists = flags.value.some((f) => f.key === overrideForm.key && f.scope === scope)
  if (exists) {
    ElMessage.warning(`账户 ${scope} 已有该开关的覆盖值，请先删除`)
    return
  }
  busy.value = `new:${overrideForm.key}:${scope}`
  try {
    await adminApi.setFlag(overrideForm.key, overrideForm.value, scope)
    ElMessage.success(`已为账户 ${scope} 设置覆盖值`)
    overrideForm.scope = ''
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    busy.value = null
  }
}

async function toggleOverride(f: FlagValue, next: boolean) {
  busy.value = `ov:${f.key}:${f.scope}`
  try {
    await adminApi.setFlag(f.key, next, f.scope)
    ElMessage.success(`账户 ${f.scope} 的覆盖值已更新`)
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    busy.value = null
  }
}

/**
 * There is no DELETE in the frozen contract for flags, so an override is
 * neutralised by writing the global value into the override scope. Documented
 * here and in README so the behaviour is not a surprise.
 */
async function clearOverride(f: FlagValue) {
  const global = globalOf(f.key)
  busy.value = `ov:${f.key}:${f.scope}`
  try {
    await adminApi.setFlag(f.key, global?.value ?? true, f.scope)
    ElMessage.success(`账户 ${f.scope} 的覆盖值已与全局对齐`)
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    busy.value = null
  }
}
</script>

<template>
  <div class="page">
    <PageHeader title="功能开关" desc="开关改动立即生效（网关热读取），不需要重启。按账户覆盖优先于全局值。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="note">
      <Icon name="bolt" :size="15" />
      <p>
        热加载：开关值保存在网关内存 + 存储层，写入后下一次请求即按新值执行。
        <b>关闭「网关总开关」会让全部客户立即 503</b>，请仅在维护窗口操作。
        当前全局开启 <span class="mono">{{ enabledCount }}/{{ FLAGS.length }}</span>。
      </p>
    </div>

    <div v-if="loading" class="stack">
      <div v-for="i in 4" :key="i" class="flag-skeleton">
        <span class="sk" style="width: 30%; height: 14px" />
        <span class="sk" style="width: 60%; height: 11px; margin-top: 8px" />
      </div>
    </div>

    <div v-else-if="error" class="panel">
      <EmptyState icon="alert" title="开关加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </div>

    <template v-else>
      <div class="flags">
        <article v-for="meta in FLAGS" :key="meta.key" class="flag" :class="{ 'flag--on': globalOf(meta.key)?.value }">
          <header class="flag__head">
            <div class="flag__title">
              <h3>{{ meta.label }}</h3>
              <code class="mono flag__key">{{ meta.key }}</code>
              <span v-if="meta.risk === 'high'" class="badge badge--warn badge--no-dot">高风险</span>
            </div>
            <el-switch
              :model-value="globalOf(meta.key)?.value ?? false"
              :loading="busy === `global:${meta.key}`"
              size="default"
              @change="(v: string | number | boolean) => toggleGlobal(meta, Boolean(v))"
            />
          </header>

          <p class="flag__desc">{{ meta.desc }}</p>

          <div class="flag__effect">
            <span class="flag__effect-label">关闭时</span>
            <span>{{ meta.offEffect }}</span>
          </div>

          <div class="flag__overrides">
            <div class="flag__ov-head">
              <span>按账户覆盖</span>
              <span class="mono dim">{{ overridesOf(meta.key).length }} 条</span>
            </div>
            <div v-if="!overridesOf(meta.key).length" class="flag__ov-empty">无覆盖，跟随全局</div>
            <div v-for="ov in overridesOf(meta.key)" :key="ov.scope" class="flag__ov-row">
              <code class="mono flag__ov-scope">account #{{ ov.scope }}</code>
              <span class="badge" :class="ov.value ? 'badge--ok' : 'badge--idle'">{{ ov.value ? '开' : '关' }}</span>
              <span class="dim flag__ov-time mono">{{ fmtDateTime(ov.updated_at) }}</span>
              <span class="flag__ov-actions">
                <el-button text size="small" :loading="busy === `ov:${ov.key}:${ov.scope}`"
                  @click="toggleOverride(ov, !ov.value)">反转</el-button>
                <el-button text size="small" @click="clearOverride(ov)">对齐全局</el-button>
              </span>
            </div>
          </div>
        </article>
      </div>

      <!-- add override -->
      <section class="panel">
        <div class="panel__head">
          <span class="panel__title">新增账户覆盖</span>
          <span class="panel__hint">用于测试租户或特殊合约客户</span>
        </div>
        <div class="panel__body">
          <div class="ov-form">
            <label class="ov-field">
              <span>开关</span>
              <el-select v-model="overrideForm.key" style="width: 100%">
                <el-option v-for="m in FLAGS" :key="m.key" :label="`${m.label}（${m.key}）`" :value="m.key" />
              </el-select>
            </label>
            <label class="ov-field">
              <span>账户 ID（scope）</span>
              <el-input v-model="overrideForm.scope" placeholder="例如 75" class="mono-input" />
            </label>
            <label class="ov-field ov-field--narrow">
              <span>值</span>
              <el-switch v-model="overrideForm.value" inline-prompt active-text="开" inactive-text="关" />
            </label>
            <el-button type="primary" :loading="busy?.startsWith('new:')" @click="addOverride">添加覆盖</el-button>
          </div>
          <p class="ov-hint">
            契约中的 <code class="mono">PUT /admin/v1/flags/{key}</code> 只接收 <code class="mono">{value, scope}</code>，
            没有删除语义；因此「对齐全局」会把覆盖值写成当前全局值，等效于移除例外。
          </p>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.btn-gap {
  margin-left: 6px;
}

.note {
  display: flex;
  gap: var(--sp-3);
  padding: var(--sp-3) var(--sp-4);
  border-radius: var(--r-sm);
  background: var(--warn-soft);
  border: 1px solid color-mix(in srgb, var(--warn) 26%, transparent);
  color: var(--warn);
}

.note p {
  font-size: var(--fs-xs);
  line-height: 1.65;
  color: var(--ink-2);
}

.note b {
  color: var(--warn);
}

.flag-skeleton {
  padding: var(--sp-4) var(--sp-5);
  background: var(--bg-panel);
  border: 1px solid var(--line-1);
  border-radius: var(--r-md);
}

/* ---- flag cards ---- */
.flags {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--sp-4);
}

.flag {
  display: flex;
  flex-direction: column;
  gap: var(--sp-3);
  padding: var(--sp-5);
  background: var(--bg-panel);
  border: 1px solid var(--line-1);
  border-radius: var(--r-md);
  border-left: 2px solid var(--line-2);
}

.flag--on {
  border-left-color: var(--accent);
}

.flag__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--sp-3);
}

.flag__title {
  display: flex;
  align-items: baseline;
  gap: var(--sp-2);
  flex-wrap: wrap;
}

.flag__title h3 {
  font-size: var(--fs-base);
  font-weight: 600;
}

.flag__key {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.flag__desc {
  font-size: var(--fs-xs);
  color: var(--ink-2);
  line-height: 1.65;
}

.flag__effect {
  display: flex;
  gap: var(--sp-2);
  font-size: var(--fs-2xs);
  color: var(--ink-3);
  padding: var(--sp-2) var(--sp-3);
  background: var(--bg-sunken);
  border-radius: var(--r-xs);
  line-height: 1.6;
}

.flag__effect-label {
  flex: 0 0 auto;
  font-weight: 600;
  color: var(--ink-4);
}

.flag__overrides {
  border-top: 1px solid var(--line-1);
  padding-top: var(--sp-3);
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.flag__ov-head {
  display: flex;
  justify-content: space-between;
  font-size: var(--fs-2xs);
  color: var(--ink-3);
  font-weight: 550;
}

.flag__ov-empty {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.flag__ov-row {
  display: flex;
  align-items: center;
  gap: var(--sp-2);
  font-size: var(--fs-2xs);
  padding: 3px 0;
}

.flag__ov-scope {
  font-family: var(--font-mono);
  color: var(--ink-2);
}

.flag__ov-time {
  font-size: var(--fs-2xs);
  margin-left: auto;
}

.flag__ov-actions {
  display: flex;
  gap: 0;
}

/* ---- override form ---- */
.ov-form {
  display: flex;
  align-items: flex-end;
  gap: var(--sp-3);
  flex-wrap: wrap;
}

.ov-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  flex: 1;
  min-width: 180px;
}

.ov-field--narrow {
  flex: 0 0 80px;
  min-width: 80px;
}

.ov-field > span {
  font-size: var(--fs-2xs);
  color: var(--ink-3);
  font-weight: 550;
}

.ov-hint {
  margin-top: var(--sp-3);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  line-height: 1.65;
}

:deep(.mono-input .el-input__inner) {
  font-family: var(--font-mono);
}

@media (max-width: 1240px) {
  .flags {
    grid-template-columns: 1fr;
  }
}
</style>
