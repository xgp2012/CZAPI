<script setup lang="ts">
/**
 * 对账 — the daily "do the books balance" screen.
 *
 * Three totals must agree: what usage cost, what subscriptions consumed, and
 * what balance was deducted. The verdict banner is the headline; the per-account
 * table exists to find *which* account drifted, so rows are sorted worst-first.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import dayjs from 'dayjs'
import { adminApi } from '@/api/admin'
import type { ReconcileReport } from '@/types/api'
import { fmtDateTime, fmtMoney, fmtMoneyLong, fmtPercent } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatCard from '@/components/StatCard.vue'
import Icon from '@/components/Icon.vue'

const day = ref(dayjs().subtract(1, 'day').format('YYYY-MM-DD'))
const report = ref<ReconcileReport | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    report.value = await adminApi.reconcile(day.value)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '对账失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const details = computed(() => {
  const list = [...(report.value?.details ?? [])]
  return list.sort((a, b) => Math.abs(b.diff_micros) - Math.abs(a.diff_micros))
})

const drifted = computed(() => details.value.filter((d) => d.diff_micros !== 0))

/** usage = subscription + balance, so the residual tells us if anything is unaccounted. */
const residual = computed(() => {
  const r = report.value
  if (!r) return 0
  return r.usage_cost_micros - r.subscription_consumed_micros - r.balance_consumed_micros
})

function quick(daysAgo: number) {
  day.value = dayjs().subtract(daysAgo, 'day').format('YYYY-MM-DD')
  load()
}

/** Rows with a difference get a tinted background so the eye lands on them. */
const rowClassName = ({ row }: { row: { diff_micros: number } }) => (row.diff_micros !== 0 ? 'row-diff' : '')
</script>

<template>
  <div class="page">
    <PageHeader title="对账" desc="usage 聚合 = 订阅消耗 + 余额扣减。差异为 0 才算平衡；有差异时下表会列出具体账户。">
      <template #actions>
        <el-button-group>
          <el-button size="small" @click="quick(1)">昨天</el-button>
          <el-button size="small" @click="quick(2)">前天</el-button>
          <el-button size="small" @click="quick(7)">7 天前</el-button>
        </el-button-group>
        <el-date-picker v-model="day" type="date" value-format="YYYY-MM-DD" :clearable="false" style="width: 150px"
          @change="load" />
        <el-button type="primary" :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">重新对账</span>
        </el-button>
      </template>
    </PageHeader>

    <div v-if="loading && !report" class="grid grid--stats">
      <div v-for="i in 4" :key="i" class="sk-card">
        <span class="sk" style="width: 60px; height: 11px" />
        <span class="sk" style="width: 110px; height: 24px; margin-top: 10px" />
      </div>
    </div>

    <div v-else-if="error" class="panel">
      <EmptyState icon="alert" title="对账失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </div>

    <template v-else-if="report">
      <!-- verdict -->
      <div class="verdict" :class="report.ok ? 'verdict--ok' : 'verdict--bad'">
        <span class="verdict__mark">
          <Icon :name="report.ok ? 'check' : 'alert'" :size="20" />
        </span>
        <div class="verdict__body">
          <h2>{{ report.ok ? '账目平衡' : '存在对账差异' }}</h2>
          <p>
            {{ report.day }} ·
            {{ report.ok
              ? '三条链路（用量 / 订阅消耗 / 余额扣减）完全一致。'
              : `有 ${drifted.length} 个账户出现偏差，合计差异 ${fmtMoneyLong(report.diff_micros)}，请逐条核对下方明细。` }}
          </p>
        </div>
        <div class="verdict__diff">
          <span class="verdict__diff-label">差异合计</span>
          <span class="verdict__diff-value mono">{{ fmtMoneyLong(report.diff_micros) }}</span>
        </div>
      </div>

      <!-- comparison -->
      <div class="grid grid--stats">
        <StatCard label="用量成本" :value="fmtMoney(report.usage_cost_micros)" tone="accent"
          :hint="'usage_record 聚合（计费引擎结算）'" />
        <StatCard label="订阅消耗" :value="fmtMoney(report.subscription_consumed_micros)"
          :hint="`占用量成本 ${fmtPercent(report.subscription_consumed_micros / (report.usage_cost_micros || 1))}`" />
        <StatCard label="余额扣减" :value="fmtMoney(report.balance_consumed_micros)"
          :hint="`占用量成本 ${fmtPercent(report.balance_consumed_micros / (report.usage_cost_micros || 1))}`" />
        <StatCard label="链路残差" :value="fmtMoneyLong(residual)"
          :tone="residual === 0 ? 'ok' : 'danger'" :hint="'用量 − 订阅 − 余额，应为 0'" />
      </div>

      <!-- detail -->
      <section class="panel">
        <div class="panel__head">
          <span class="panel__title">按账户明细</span>
          <span class="panel__hint">
            共 {{ details.length }} 个账户 ·
            <span v-if="drifted.length" class="hint-bad">{{ drifted.length }} 个有差异</span>
            <span v-else class="hint-ok">全部一致</span>
          </span>
        </div>

        <EmptyState v-if="!details.length" compact icon="scale" title="这一天没有用量记录" desc="选择有流量的日期再试。" />

        <el-table v-else :data="details" row-key="account_id" :row-class-name="rowClassName">
          <el-table-column label="账户" width="120">
            <template #default="{ row }"><span class="mono">#{{ row.account_id }}</span></template>
          </el-table-column>

          <el-table-column label="V10 host" width="110">
            <template #default="{ row }"><code class="mono">{{ row.v10_host_id }}</code></template>
          </el-table-column>

          <el-table-column label="用量成本" width="150" align="right" class-name="col-num">
            <template #default="{ row }"><span class="mono">{{ fmtMoneyLong(row.usage_micros) }}</span></template>
          </el-table-column>

          <el-table-column label="账本扣减" width="150" align="right" class-name="col-num">
            <template #default="{ row }"><span class="mono">{{ fmtMoneyLong(row.ledger_micros) }}</span></template>
          </el-table-column>

          <el-table-column label="差异" width="140" align="right" class-name="col-num">
            <template #default="{ row }">
              <span class="mono diff" :class="row.diff_micros === 0 ? 'diff--ok' : 'diff--bad'">
                {{ row.diff_micros === 0 ? '0.00' : fmtMoneyLong(row.diff_micros) }}
              </span>
            </template>
          </el-table-column>

          <el-table-column label="结论" width="120">
            <template #default="{ row }">
              <span class="badge" :class="row.diff_micros === 0 ? 'badge--ok' : 'badge--danger'">
                {{ row.diff_micros === 0 ? '一致' : '不一致' }}
              </span>
            </template>
          </el-table-column>

          <el-table-column label="说明" min-width="220">
            <template #default="{ row }">
              <span v-if="row.diff_micros === 0" class="dim">—</span>
              <span v-else class="dim">
                账本比用量少 {{ fmtMoneyLong(Math.abs(row.diff_micros)) }}，可能是结算未落库或重复退款
              </span>
            </template>
          </el-table-column>
        </el-table>
      </section>

      <p class="footnote">
        对账结果由后端脚本按天计算，前端只做展示 · 生成于 {{ fmtDateTime(Math.floor(Date.now() / 1000)) }}
      </p>
    </template>
  </div>
</template>

<style scoped>
.btn-gap {
  margin-left: 6px;
}

.grid--stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--sp-4);
}

.sk-card {
  display: flex;
  flex-direction: column;
  padding: var(--sp-4) var(--sp-5);
  background: var(--bg-panel);
  border: 1px solid var(--line-1);
  border-radius: var(--r-md);
}

/* ---- verdict ---- */
.verdict {
  display: flex;
  align-items: center;
  gap: var(--sp-4);
  padding: var(--sp-5);
  border-radius: var(--r-md);
  border: 1px solid transparent;
}

.verdict--ok {
  background: var(--ok-soft);
  border-color: color-mix(in srgb, var(--ok) 28%, transparent);
}

.verdict--bad {
  background: var(--danger-soft);
  border-color: color-mix(in srgb, var(--danger) 32%, transparent);
}

.verdict__mark {
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  flex: 0 0 40px;
  border-radius: 50%;
  background: var(--bg-panel);
}

.verdict--ok .verdict__mark {
  color: var(--ok);
  border: 1px solid color-mix(in srgb, var(--ok) 40%, transparent);
}

.verdict--bad .verdict__mark {
  color: var(--danger);
  border: 1px solid color-mix(in srgb, var(--danger) 40%, transparent);
}

.verdict__body h2 {
  font-size: var(--fs-md);
  font-weight: 620;
}

.verdict--ok .verdict__body h2 {
  color: var(--ok);
}

.verdict--bad .verdict__body h2 {
  color: var(--danger);
}

.verdict__body p {
  font-size: var(--fs-xs);
  color: var(--ink-2);
  margin-top: 2px;
  line-height: 1.6;
}

.verdict__diff {
  margin-left: auto;
  text-align: right;
  display: flex;
  flex-direction: column;
  gap: 0;
}

.verdict__diff-label {
  font-size: var(--fs-2xs);
  color: var(--ink-3);
}

.verdict__diff-value {
  font-size: var(--fs-lg);
  font-weight: 600;
  color: var(--ink-1);
}

.hint-ok {
  color: var(--ok);
}

.hint-bad {
  color: var(--danger);
  font-weight: 600;
}

.diff {
  font-weight: 600;
}

.diff--ok {
  color: var(--ink-3);
}

.diff--bad {
  color: var(--danger);
}

:deep(.row-diff) {
  background: color-mix(in srgb, var(--danger) 5%, transparent);
}

.footnote {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

@media (max-width: 1280px) {
  .grid--stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
