<script setup lang="ts">
/**
 * 账户与 Key — the operator's main working screen.
 *
 * Layout: searchable account table on the left; a detail drawer that stacks
 *   identity → 30-day usage → subscriptions → keys.
 * Two write paths carry real risk and get explicit friction:
 *   · 调账 (adjust) requires 备注 + ref_id because the pair is the idempotency key
 *   · 封禁 a key asks for confirmation and names the account it belongs to
 */
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import dayjs from 'dayjs'
import { adminApi } from '@/api/admin'
import type { Account, AccountDetail, AdminKey } from '@/types/api'
import {
  fmtDateTime,
  fmtInt,
  fmtMoney,
  fmtMoneyLong,
  fmtPercent,
  fmtRelative,
  fmtTokens,
  yuanToMicros,
} from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatCard from '@/components/StatCard.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import LineChart, { type LineSeries } from '@/components/LineChart.vue'
import Icon from '@/components/Icon.vue'

/* ------------------------------------------------------------------ */
/* Accounts list                                                      */
/* ------------------------------------------------------------------ */

const rows = ref<Account[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const q = ref('')
const loading = ref(false)
const error = ref<string | null>(null)

async function loadAccounts() {
  loading.value = true
  error.value = null
  try {
    const res = await adminApi.listAccounts(q.value.trim(), page.value, pageSize.value)
    rows.value = res.items
    total.value = res.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载账户失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 1
  loadAccounts()
}

function onPage(p: number) {
  page.value = p
  loadAccounts()
}

/* ------------------------------------------------------------------ */
/* Detail drawer                                                      */
/* ------------------------------------------------------------------ */

const drawerOpen = ref(false)
const detail = ref<AccountDetail | null>(null)
const detailLoading = ref(false)
const activeAccountId = ref<number | null>(null)

async function openDetail(row: Account) {
  activeAccountId.value = row.account_id
  drawerOpen.value = true
  await loadDetail(row.account_id)
}

async function loadDetail(id: number) {
  detailLoading.value = true
  try {
    detail.value = await adminApi.getAccount(id)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '加载账户详情失败')
  } finally {
    detailLoading.value = false
  }
}

watch(drawerOpen, (open) => {
  if (!open) detail.value = null
})

const trendCategories = computed(() => (detail.value?.trend ?? []).map((p) => p.day.slice(5)))
const trendSeries = computed<LineSeries[]>(() => {
  const t = detail.value?.trend ?? []
  return [
    { name: '请求', data: t.map((p) => p.requests), tone: 'steel', bar: true },
    { name: '消费（元）', data: t.map((p) => Math.round((p.cost_micros / 1_000_000) * 100) / 100), tone: 'accent', axis: 'right' },
  ]
})

/* ---- adjust dialog ---- */

const adjustOpen = ref(false)
const adjusting = ref(false)
const adjustForm = reactive({
  amountYuan: 0 as number,
  remark: '',
  ref_id: '',
})
const amountMicros = computed(() => yuanToMicros(adjustForm.amountYuan))

function openAdjust() {
  adjustForm.amountYuan = 0
  adjustForm.remark = ''
  adjustForm.ref_id = `ADJ-${dayjs().format('YYYYMMDD')}-${Math.floor(Math.random() * 9000 + 1000)}`
  adjustOpen.value = true
}

async function submitAdjust() {
  if (!detail.value) return
  if (!adjustForm.amountYuan) {
    ElMessage.warning('请输入调整金额（可为负数）')
    return
  }
  if (!adjustForm.remark.trim()) {
    ElMessage.warning('备注必填：它会进入账本，供日后核对')
    return
  }
  if (!adjustForm.ref_id.trim()) {
    ElMessage.warning('ref_id 必填：同一凭证只能入账一次')
    return
  }
  adjusting.value = true
  try {
    const res = await adminApi.adjustAccount(detail.value.account_id, {
      amount_micros: amountMicros.value,
      remark: adjustForm.remark.trim(),
      ref_id: adjustForm.ref_id.trim(),
    })
    if (res.applied) {
      ElMessage.success(`已调账，新余额 ${fmtMoneyLong(res.balance_micros)}`)
    } else {
      ElMessage.warning('该凭证此前已处理（幂等命中），余额未变化')
    }
    adjustOpen.value = false
    await loadDetail(detail.value.account_id)
    await loadAccounts()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '调账失败')
  } finally {
    adjusting.value = false
  }
}

/* ---- account status ---- */

const statusBusy = ref(false)

async function toggleAccountStatus(next: boolean) {
  if (!detail.value) return
  statusBusy.value = true
  try {
    await adminApi.updateAccount(detail.value.account_id, { status: next ? 1 : 0 })
    detail.value = { ...detail.value, status: next ? 1 : 0 }
    ElMessage.success(next ? '账户已启用' : '账户已禁用，其下所有 Key 立即失效')
    await loadAccounts()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    statusBusy.value = false
  }
}

async function toggleAllowNegative(next: boolean) {
  if (!detail.value) return
  statusBusy.value = true
  try {
    await adminApi.updateAccount(detail.value.account_id, { allow_negative: next })
    detail.value = { ...detail.value, allow_negative: next }
    ElMessage.success(next ? '已允许余额为负' : '已禁止余额为负')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    statusBusy.value = false
  }
}

/* ------------------------------------------------------------------ */
/* Cross-account key search                                           */
/* ------------------------------------------------------------------ */

const keyQuery = ref('')
const keyStatus = ref<'' | 'active' | 'suspended' | 'revoked'>('')
const keyRows = ref<AdminKey[]>([])
const keyTotal = ref(0)
const keyPage = ref(1)
const keyLoading = ref(false)

async function searchKeys() {
  keyLoading.value = true
  try {
    const res = await adminApi.searchKeys(keyQuery.value.trim(), keyStatus.value, undefined, keyPage.value, 20)
    keyRows.value = res.items
    keyTotal.value = res.total
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '检索 Key 失败')
  } finally {
    keyLoading.value = false
  }
}

async function banKey(row: AdminKey, ban: boolean) {
  try {
    await ElMessageBox.confirm(
      ban
        ? `封禁「${row.name}」（${row.key_prefix}…）后，调用方会立刻收到 401。可随时解封。`
        : `解封「${row.name}」（${row.key_prefix}…）？解封后该 Key 立即恢复调用。`,
      ban ? '确认封禁 Key' : '确认解封 Key',
      { confirmButtonText: ban ? '封禁' : '解封', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  try {
    await adminApi.setKeyStatus(row.key_id, ban ? 'suspended' : 'active')
    ElMessage.success(ban ? '已封禁' : '已解封')
    await searchKeys()
    if (detail.value) await loadDetail(detail.value.account_id)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  }
}

const keyStatusTone = (s: string) => (s === 'active' ? 'ok' : s === 'suspended' ? 'warn' : 'danger')

onMounted(() => {
  loadAccounts()
  searchKeys()
})
</script>

<template>
  <div class="page">
    <PageHeader title="账户与 Key" desc="按邮箱或 V10 client_id 检索账户，进入详情可查看订阅、Key 与近 30 天用量并执行调账。">
      <template #actions>
        <el-button :loading="loading" @click="loadAccounts">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-input v-model="q" placeholder="搜索邮箱 / client_id / account_id" clearable style="width: 320px"
        @keyup.enter="search" @clear="search">
        <template #prefix><Icon name="search" :size="14" /></template>
      </el-input>
      <el-button @click="search">搜索</el-button>
      <span class="toolbar__spacer" />
      <span class="toolbar__note">共 <b class="mono">{{ total }}</b> 个账户</span>
    </div>

    <section v-if="loading && !rows.length" class="panel">
      <div class="panel__body">
        <div v-for="i in 6" :key="i" class="row-skeleton">
          <span class="sk" style="width: 24%; height: 12px" />
          <span class="sk" style="width: 12%; height: 12px" />
          <span class="sk" style="width: 14%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error && !rows.length" class="panel">
      <EmptyState icon="alert" title="账户加载失败" :desc="error">
        <el-button size="small" @click="loadAccounts">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!rows.length" class="panel">
      <EmptyState icon="users" title="没有匹配的账户" desc="账户由 V10 下单后自动创建，也可以换一个关键词再试。">
        <el-button size="small" @click="q = ''; search()">清空搜索</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="rows" row-key="account_id" highlight-current-row @row-click="openDetail">
        <el-table-column label="账户" min-width="230">
          <template #default="{ row }">
            <div class="cell-acc">
              <span class="cell-acc__email mono">{{ row.email }}</span>
              <span class="cell-acc__id mono">account #{{ row.account_id }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="V10 client" width="130">
          <template #default="{ row }"><code class="mono">{{ row.v10_client_id }}</code></template>
        </el-table-column>

        <el-table-column label="余额" width="140" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono balance" :class="{ 'is-negative': row.balance_micros < 0 }">{{ fmtMoneyLong(row.balance_micros) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="允许负余额" width="112">
          <template #default="{ row }">
            <span class="badge" :class="row.allow_negative ? 'badge--warn' : 'badge--idle'">{{ row.allow_negative ? '是' : '否' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <span class="badge" :class="row.status === 1 ? 'badge--ok' : 'badge--danger'">{{ row.status === 1 ? '正常' : '禁用' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="注册时间" width="140">
          <template #default="{ row }"><span class="mono dim">{{ fmtDateTime(row.created_at) }}</span></template>
        </el-table-column>

        <el-table-column label="" width="104" align="right">
          <template #default="{ row }">
            <el-button text size="small" @click.stop="openDetail(row)">详情 →</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination :current-page="page" :page-size="pageSize" :total="total" layout="total, prev, pager, next"
        background @current-change="onPage" />
    </section>

    <!-- ============ cross-account key search ============ -->
    <section class="panel">
      <div class="panel__head">
        <span class="panel__title">跨账户 Key 检索</span>
        <span class="panel__hint">按前缀 / 名称 / 邮箱搜索，可直接封禁</span>
      </div>

      <div class="keybar">
        <el-input v-model="keyQuery" placeholder="sk-… / 名称 / 邮箱" clearable style="width: 260px" @keyup.enter="keyPage = 1; searchKeys()"
          @clear="keyPage = 1; searchKeys()">
          <template #prefix><Icon name="search" :size="14" /></template>
        </el-input>
        <el-select v-model="keyStatus" placeholder="全部状态" clearable style="width: 140px" @change="keyPage = 1; searchKeys()">
          <el-option label="生效" value="active" />
          <el-option label="停用" value="suspended" />
          <el-option label="吊销" value="revoked" />
        </el-select>
        <el-button :loading="keyLoading" @click="keyPage = 1; searchKeys()">检索</el-button>
        <span class="toolbar__spacer" />
        <span class="toolbar__note">共 <b class="mono">{{ keyTotal }}</b> 把</span>
      </div>

      <div v-if="keyLoading && !keyRows.length" class="panel__body">
        <div v-for="i in 4" :key="i" class="row-skeleton"><span class="sk" style="width: 40%; height: 12px" /></div>
      </div>
      <EmptyState v-else-if="!keyRows.length" compact icon="key" title="没有匹配的 Key" desc="换一个关键词，或清空筛选。" />
      <template v-else>
        <el-table :data="keyRows" row-key="key_id">
          <el-table-column label="Key" min-width="220">
            <template #default="{ row }">
              <div class="cell-acc">
                <span class="cell-acc__email">{{ row.name }}</span>
                <code class="cell-acc__id mono">{{ row.key_prefix }}…</code>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="归属账户" min-width="180">
            <template #default="{ row }">
              <span class="mono">{{ row.email ?? '—' }}</span>
              <span class="mono dim"> #{{ row.account_id }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <span class="badge" :class="`badge--${keyStatusTone(row.status)}`">
                {{ row.status === 'active' ? '生效' : row.status === 'suspended' ? '停用' : '吊销' }}
              </span>
            </template>
          </el-table-column>
          <el-table-column label="最近使用" width="126">
            <template #default="{ row }"><span>{{ fmtRelative(row.last_used_at) }}</span></template>
          </el-table-column>
          <el-table-column label="30 天消费" width="130" align="right" class-name="col-num">
            <template #default="{ row }"><span class="mono">{{ fmtMoney(row.usage_micros_30d) }}</span></template>
          </el-table-column>
          <el-table-column label="操作" width="112" align="right">
            <template #default="{ row }">
              <el-button v-if="row.status === 'active'" text size="small" type="danger" @click="banKey(row, true)">封禁</el-button>
              <el-button v-else-if="row.status === 'suspended'" text size="small" @click="banKey(row, false)">解封</el-button>
              <span v-else class="dim">—</span>
            </template>
          </el-table-column>
        </el-table>
        <el-pagination :current-page="keyPage" :page-size="20" :total="keyTotal" layout="total, prev, pager, next"
          background @current-change="(p: number) => { keyPage = p; searchKeys() }" />
      </template>
    </section>

    <!-- ============ detail drawer ============ -->
    <el-drawer v-model="drawerOpen" size="62%" :title="detail ? `账户 ${detail.email}` : '账户详情'">
      <div v-if="detailLoading && !detail" class="stack">
        <span class="sk" style="width: 60%; height: 18px" />
        <span class="sk" style="width: 100%; height: 90px" />
        <span class="sk" style="width: 100%; height: 180px" />
      </div>

      <div v-else-if="detail" class="detail">
        <!-- identity -->
        <div class="detail__id">
          <div class="detail__id-main">
            <span class="mono detail__email">{{ detail.email }}</span>
            <span class="detail__meta mono">
              account #{{ detail.account_id }} · client {{ detail.v10_client_id }} · 注册 {{ fmtDateTime(detail.created_at) }}
            </span>
          </div>
          <div class="detail__id-actions">
            <el-switch :model-value="detail.status === 1" :loading="statusBusy" inline-prompt active-text="启用"
              inactive-text="禁用" @change="(v: string | number | boolean) => toggleAccountStatus(Boolean(v))" />
            <el-button type="primary" size="small" @click="openAdjust">调账</el-button>
          </div>
        </div>

        <div class="grid grid--stats">
          <StatCard label="当前余额" :value="fmtMoneyLong(detail.balance_micros)" tone="accent"
            :hint="detail.allow_negative ? '允许为负' : '不允许为负'" />
          <StatCard label="近 30 天消费" :value="fmtMoney(detail.usage_30d.cost_micros)" :hint="`${fmtInt(detail.usage_30d.requests)} 次请求`" />
          <StatCard label="近 30 天 Token" :value="fmtTokens(detail.usage_30d.prompt_tokens + detail.usage_30d.completion_tokens)"
            :hint="`入 ${fmtTokens(detail.usage_30d.prompt_tokens)} · 出 ${fmtTokens(detail.usage_30d.completion_tokens)}`" />
          <StatCard label="订阅数" :value="String(detail.subscriptions.length)" :hint="`Key ${detail.keys.length} 把`" />
        </div>

        <div class="detail__toggle">
          <el-switch :model-value="detail.allow_negative" :loading="statusBusy" inline-prompt active-text="允许负余额"
            inactive-text="禁止负余额" @change="(v: string | number | boolean) => toggleAllowNegative(Boolean(v))" />
          <span class="dim">允许为负时，余额耗尽仍可调用，欠费在下次充值中抵扣。</span>
        </div>

        <!-- trend -->
        <section class="panel">
          <div class="panel__head"><span class="panel__title">近 30 天用量</span></div>
          <div class="panel__body panel__body--chart">
            <LineChart v-if="detail.trend?.length" :categories="trendCategories" :series="trendSeries" :height="180"
              y-name="请求" y-name-right="元" />
            <EmptyState v-else compact icon="chart" title="暂无用量" />
          </div>
        </section>

        <!-- subscriptions -->
        <section class="panel">
          <div class="panel__head">
            <span class="panel__title">订阅</span>
            <span class="panel__hint">{{ detail.subscriptions.length }} 条</span>
          </div>
          <EmptyState v-if="!detail.subscriptions.length" compact icon="shield" title="该账户还没有订阅"
            desc="V10 下单后会自动开通；也可以只用余额计费。" />
          <div v-else class="subs">
            <div v-for="s in detail.subscriptions" :key="s.subscription_id" class="sub-row">
              <div class="sub-row__head">
                <div class="sub-row__name">
                  <b>{{ s.plan_name }}</b>
                  <code class="mono">{{ s.plan_code }}</code>
                  <span class="badge" :class="s.status === 'active' ? 'badge--ok' : s.status === 'suspended' ? 'badge--warn' : 'badge--idle'">
                    {{ s.status }}
                  </span>
                </div>
                <span class="mono dim">#{{ s.subscription_id }} · host {{ s.v10_host_id }}</span>
              </div>
              <QuotaMeter :used="s.quota_used" :total="s.quota_total" show-labels />
              <div class="sub-row__foot mono dim">
                {{ fmtDateTime(s.period_start) }} → {{ fmtDateTime(s.period_end) }} · 已用
                {{ fmtPercent(s.quota_used / (s.quota_total || 1)) }}
              </div>
            </div>
          </div>
        </section>

        <!-- keys -->
        <section class="panel">
          <div class="panel__head">
            <span class="panel__title">Key</span>
            <span class="panel__hint">{{ detail.keys.length }} 把</span>
          </div>
          <EmptyState v-if="!detail.keys.length" compact icon="key" title="该账户还没有 Key" />
          <table v-else class="mini-table">
            <thead><tr><th>名称</th><th>前缀</th><th>状态</th><th class="tar">30 天</th><th class="tar">最近使用</th></tr></thead>
            <tbody>
              <tr v-for="k in detail.keys" :key="k.key_id">
                <td>{{ k.name }}</td>
                <td><code class="mono">{{ k.key_prefix }}…</code></td>
                <td>
                  <span class="badge" :class="`badge--${keyStatusTone(k.status)}`">
                    {{ k.status === 'active' ? '生效' : k.status === 'suspended' ? '停用' : '吊销' }}
                  </span>
                </td>
                <td class="tar mono">{{ fmtMoney(k.usage_micros_30d) }}</td>
                <td class="tar dim">{{ fmtRelative(k.last_used_at) }}</td>
              </tr>
            </tbody>
          </table>
        </section>
      </div>
    </el-drawer>

    <!-- ============ adjust dialog ============ -->
    <el-dialog v-model="adjustOpen" title="账户调账" width="520px" append-to-body>
      <el-alert type="warning" :closable="false" show-icon style="margin-bottom: 16px">
        调账会写入 <code class="mono">credit_ledger</code>，凭证 <code class="mono">(ref_type=admin_adjust, ref_id)</code>
        唯一 —— 同一个 ref_id 重复提交不会重复入账。
      </el-alert>

      <el-form label-position="top" @submit.prevent>
        <el-form-item label="调整金额（元，正数入账 / 负数扣减）">
          <el-input-number v-model="adjustForm.amountYuan" :step="10" :precision="2" controls-position="right"
            style="width: 100%" />
          <div class="hint">
            换算为 <span class="mono">{{ fmtInt(amountMicros) }}</span> 微元（1 元 = 1,000,000）
            <span v-if="detail">；调整后余额约
              <span class="mono">{{ fmtMoneyLong(detail.balance_micros + amountMicros) }}</span>
            </span>
          </div>
        </el-form-item>

        <el-form-item label="备注（必填，进账本）">
          <el-input v-model="adjustForm.remark" placeholder="例如：工单 #2231 超时扣费补偿" maxlength="120" show-word-limit />
        </el-form-item>

        <el-form-item label="ref_id（幂等凭证，必填）">
          <el-input v-model="adjustForm.ref_id" class="mono-input" placeholder="ADJ-20261105-0001" />
          <div class="hint">建议格式 ADJ-日期-序号，便于与工单对账。</div>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="adjustOpen = false">取消</el-button>
        <el-button type="primary" :loading="adjusting" @click="submitAdjust">确认调账</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.btn-gap {
  margin-left: 6px;
}

.toolbar,
.keybar {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  flex-wrap: wrap;
}

.keybar {
  padding: var(--sp-4) var(--sp-5);
  border-bottom: 1px solid var(--line-1);
}

.toolbar__spacer {
  flex: 1;
}

.toolbar__note {
  font-size: var(--fs-xs);
  color: var(--ink-4);
}

.row-skeleton {
  display: flex;
  gap: var(--sp-6);
  padding: 12px 0;
  border-bottom: 1px solid var(--line-1);
}

.cell-acc {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.cell-acc__email {
  color: var(--ink-1);
  font-weight: 550;
}

.cell-acc__id {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.balance {
  color: var(--ink-1);
  font-weight: 550;
}

.balance.is-negative {
  color: var(--danger);
}

:deep(.el-table__row) {
  cursor: pointer;
}

/* ---- drawer content ---- */
.detail {
  display: flex;
  flex-direction: column;
  gap: var(--sp-4);
}

.detail__id {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--sp-4);
  padding-bottom: var(--sp-4);
  border-bottom: 1px solid var(--line-1);
}

.detail__id-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.detail__email {
  font-size: var(--fs-md);
  color: var(--ink-1);
  font-weight: 600;
}

.detail__meta {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.detail__id-actions {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
}

.detail__toggle {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  font-size: var(--fs-2xs);
  color: var(--ink-3);
}

.grid--stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--sp-3);
}

.panel__body--chart {
  padding: var(--sp-3);
}

/* ---- subscriptions ---- */
.subs {
  display: flex;
  flex-direction: column;
}

.sub-row {
  display: flex;
  flex-direction: column;
  gap: var(--sp-2);
  padding: var(--sp-4) var(--sp-5);
  border-bottom: 1px solid var(--line-1);
}

.sub-row:last-child {
  border-bottom: none;
}

.sub-row__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-3);
}

.sub-row__name {
  display: flex;
  align-items: baseline;
  gap: var(--sp-2);
}

.sub-row__name b {
  font-size: var(--fs-sm);
  color: var(--ink-1);
}

.sub-row__name code {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.sub-row__foot {
  font-size: var(--fs-2xs);
}

/* ---- mini table ---- */
.mini-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--fs-xs);
}

.mini-table th {
  text-align: left;
  font-size: var(--fs-2xs);
  color: var(--ink-3);
  font-weight: 600;
  padding: 8px var(--sp-5);
  background: var(--bg-sunken);
  border-bottom: 1px solid var(--line-2);
}

.mini-table td {
  padding: 9px var(--sp-5);
  border-bottom: 1px solid var(--line-1);
  color: var(--ink-2);
}

.mini-table tr:last-child td {
  border-bottom: none;
}

.mini-table .tar {
  text-align: right;
}

.hint {
  margin-top: 4px;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  line-height: 1.55;
}

:deep(.mono-input .el-input__inner) {
  font-family: var(--font-mono);
}

@media (max-width: 1400px) {
  .grid--stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
