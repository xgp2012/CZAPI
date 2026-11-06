<script setup lang="ts">
/**
 * 仪表盘 — operator's first screen.
 *
 * Order of reading: is anything broken (reconcile banner, breaker badges) →
 * what is happening now (QPS / error rate / TTFT) → where the money goes (top
 * models / accounts) → the 30-day shape of traffic.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useDashboardStore } from '@/stores/dashboard'
import type { Dashboard } from '@/types/api'
import { fmtCompact, fmtDateTime, fmtInt, fmtMoney, fmtMoneyLong, fmtMs, fmtPercent } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import StatCard from '@/components/StatCard.vue'
import LineChart, { type LineSeries } from '@/components/LineChart.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const store = useDashboardStore()
const data = ref<Dashboard | null>(null)
const error = ref<string | null>(null)

const loading = computed(() => store.loading && !data.value)

async function load() {
  error.value = null
  try {
    data.value = await store.load(true)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
    ElMessage.error(error.value)
  }
}

onMounted(load)

const categories = computed(() => (data.value?.trend ?? []).map((p) => p.day.slice(5)))

const series = computed<LineSeries[]>(() => {
  const trend = data.value?.trend ?? []
  return [
    { name: '请求数', data: trend.map((p) => p.requests), tone: 'steel', bar: true },
    { name: '收入（元）', data: trend.map((p) => Math.round((p.cost_micros / 1_000_000) * 100) / 100), tone: 'accent', axis: 'right' },
  ]
})

const breached = computed(() => (data.value?.channels ?? []).filter((c) => c.open))

const totalModelCost = computed(() => (data.value?.top_models ?? []).reduce((a, b) => a + b.cost_micros, 0))
const totalAccountCost = computed(() => (data.value?.top_accounts ?? []).reduce((a, b) => a + b.cost_micros, 0))

const errorTone = computed(() => {
  const r = data.value?.error_rate_5m ?? 0
  return r > 0.05 ? 'danger' : r > 0.02 ? 'warn' : 'plain'
})
</script>

<template>
  <div class="page">
    <PageHeader title="仪表盘" desc="近 5 分钟的运行指标与今日消费，以及每天的对账状态。金额单位元。">
      <template #actions>
        <el-button :loading="store.loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <!-- reconcile banner -->
    <div v-if="data" class="banner" :class="data.reconcile.ok ? 'banner--ok' : 'banner--danger'">
      <Icon :name="data.reconcile.ok ? 'check' : 'alert'" :size="16" />
      <span class="banner__text">
        <b>{{ data.reconcile.ok ? '对账平衡' : '对账存在差异' }}</b>
        <span class="banner__sep">·</span>
        最近运行 {{ fmtDateTime(data.reconcile.last_run) }}
        <span class="banner__sep">·</span>
        差异 <span class="mono">{{ fmtMoneyLong(data.reconcile.diff_micros) }}</span>
      </span>
      <RouterLink to="/admin/reconcile" class="banner__link">查看明细 →</RouterLink>
    </div>

    <div v-if="loading" class="grid grid--kpi">
      <div v-for="i in 5" :key="i" class="kpi-skeleton">
        <span class="sk" style="width: 60px; height: 11px" />
        <span class="sk" style="width: 100px; height: 26px; margin-top: 10px" />
      </div>
    </div>

    <div v-else-if="error" class="panel">
      <EmptyState icon="alert" title="仪表盘加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </div>

    <template v-else-if="data">
      <div class="grid grid--kpi">
        <StatCard label="今日请求" :value="fmtCompact(data.requests_today)" :hint="`活跃 Key ${data.active_keys} · 订阅 ${data.active_subscriptions}`" />
        <StatCard label="今日消费" :value="fmtMoney(data.cost_today_micros)" tone="accent" :hint="`本月 ${fmtMoney(data.cost_month_micros)}`" />
        <StatCard label="错误率（5 分钟）" :value="fmtPercent(data.error_rate_5m)" :tone="errorTone === 'plain' ? 'plain' : 'danger'" :hint="`账户 ${data.accounts} 个`" />
        <StatCard label="QPS（5 分钟）" :value="data.qps_5m.toFixed(2)" :hint="'请求 / 秒'" />
        <StatCard label="TTFT P95（5 分钟）" :value="fmtMs(data.ttft_p95_ms_5m)" :hint="'首字节延迟'" />
      </div>

      <div class="split">
        <!-- channel health -->
        <section class="panel">
          <div class="panel__head">
            <span class="panel__title">渠道健康</span>
            <span class="panel__hint">
              <span v-if="breached.length" class="badge badge--danger">{{ breached.length }} 个熔断</span>
              <span v-else class="badge badge--ok">全部正常</span>
            </span>
          </div>

          <div v-if="!data.channels.length" class="panel__body">
            <EmptyState compact icon="server" title="还没有配置渠道" desc="在「渠道管理」中添加第一个上游后，这里会显示熔断状态与错误率。" />
          </div>

          <table v-else class="mini-table">
            <thead>
              <tr><th>渠道</th><th>状态</th><th class="tar">错误率</th><th class="tar">熔断</th></tr>
            </thead>
            <tbody>
              <tr v-for="c in data.channels" :key="c.channel_id">
                <td>
                  <span class="chan">{{ c.name }}</span>
                  <span class="mono dim">#{{ c.channel_id }}</span>
                </td>
                <td>
                  <span class="badge" :class="c.status === 1 ? 'badge--ok' : 'badge--idle'">
                    {{ c.status === 1 ? '启用' : '停用' }}
                  </span>
                </td>
                <td class="tar mono" :class="{ 'is-hot': c.error_rate > 0.05 }">{{ fmtPercent(c.error_rate) }}</td>
                <td class="tar">
                  <span class="badge" :class="c.open ? 'badge--danger' : 'badge--ok'">{{ c.open ? 'OPEN' : 'CLOSED' }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- reconcile + counts -->
        <section class="panel">
          <div class="panel__head"><span class="panel__title">规模</span></div>
          <div class="panel__body counts">
            <div class="count">
              <span class="count__label">活跃 Key</span>
              <span class="count__value mono">{{ fmtInt(data.active_keys) }}</span>
            </div>
            <div class="count">
              <span class="count__label">活跃订阅</span>
              <span class="count__value mono">{{ fmtInt(data.active_subscriptions) }}</span>
            </div>
            <div class="count">
              <span class="count__label">账户总数</span>
              <span class="count__value mono">{{ fmtInt(data.accounts) }}</span>
            </div>
            <div class="count">
              <span class="count__label">本月消费</span>
              <span class="count__value mono accent">{{ fmtMoney(data.cost_month_micros) }}</span>
            </div>
          </div>
        </section>
      </div>

      <!-- trend -->
      <section class="panel">
        <div class="panel__head">
          <span class="panel__title">近 30 天流量与收入</span>
          <span class="panel__hint mono">{{ data.trend[0]?.day }} → {{ data.trend[data.trend.length - 1]?.day }}</span>
        </div>
        <div class="panel__body panel__body--chart">
          <LineChart :categories="categories" :series="series" :height="270" y-name="请求" y-name-right="元" />
        </div>
      </section>

      <!-- tops -->
      <div class="split">
        <section class="panel">
          <div class="panel__head">
            <span class="panel__title">Top 模型（今日）</span>
            <span class="panel__hint mono">{{ fmtMoney(totalModelCost) }}</span>
          </div>
          <table class="mini-table">
            <thead><tr><th>模型</th><th class="tar">请求</th><th class="tar">消费</th><th class="tar">占比</th></tr></thead>
            <tbody>
              <tr v-for="m in data.top_models" :key="m.model">
                <td><code class="mono">{{ m.model }}</code></td>
                <td class="tar mono">{{ fmtInt(m.requests) }}</td>
                <td class="tar mono">{{ fmtMoney(m.cost_micros) }}</td>
                <td class="tar mono dim">{{ fmtPercent(m.cost_micros / (totalModelCost || 1), 0) }}</td>
              </tr>
            </tbody>
          </table>
        </section>

        <section class="panel">
          <div class="panel__head">
            <span class="panel__title">Top 账户（今日）</span>
            <span class="panel__hint mono">{{ fmtMoney(totalAccountCost) }}</span>
          </div>
          <table class="mini-table">
            <thead><tr><th>账户</th><th class="tar">消费</th><th class="tar">占比</th></tr></thead>
            <tbody>
              <tr v-for="a in data.top_accounts" :key="a.account_id">
                <td>
                  <span class="mono">{{ a.email }}</span>
                  <span class="mono dim">client {{ a.v10_client_id }}</span>
                </td>
                <td class="tar mono">{{ fmtMoney(a.cost_micros) }}</td>
                <td class="tar mono dim">{{ fmtPercent(a.cost_micros / (totalAccountCost || 1), 0) }}</td>
              </tr>
            </tbody>
          </table>
        </section>
      </div>
    </template>
  </div>
</template>

<style scoped>
.btn-gap {
  margin-left: 6px;
}

.grid--kpi {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: var(--sp-4);
}

.kpi-skeleton {
  display: flex;
  flex-direction: column;
  padding: var(--sp-4) var(--sp-5);
  background: var(--bg-panel);
  border: 1px solid var(--line-1);
  border-radius: var(--r-md);
}

/* ---- banner ---- */
.banner {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  padding: 10px var(--sp-4);
  border-radius: var(--r-sm);
  font-size: var(--fs-xs);
  border: 1px solid transparent;
}

.banner--ok {
  background: var(--ok-soft);
  border-color: color-mix(in srgb, var(--ok) 26%, transparent);
  color: var(--ok);
}

.banner--danger {
  background: var(--danger-soft);
  border-color: color-mix(in srgb, var(--danger) 30%, transparent);
  color: var(--danger);
}

.banner__text {
  color: var(--ink-2);
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.banner__text b {
  color: inherit;
  font-weight: 600;
}

.banner--ok .banner__text b {
  color: var(--ok);
}

.banner--danger .banner__text b {
  color: var(--danger);
}

.banner__sep {
  color: var(--ink-4);
}

.banner__link {
  margin-left: auto;
  font-size: var(--fs-xs);
  color: inherit;
}

/* ---- split ---- */
.split {
  display: grid;
  grid-template-columns: minmax(0, 1.35fr) minmax(0, 1fr);
  gap: var(--sp-4);
  align-items: start;
}

.panel__body--chart {
  padding: var(--sp-4) var(--sp-4) var(--sp-2);
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
  font-weight: 600;
  color: var(--ink-3);
  padding: 8px var(--sp-5);
  background: var(--bg-sunken);
  border-bottom: 1px solid var(--line-2);
}

.mini-table td {
  padding: 9px var(--sp-5);
  border-bottom: 1px solid var(--line-1);
  color: var(--ink-2);
  vertical-align: middle;
}

.mini-table tr:last-child td {
  border-bottom: none;
}

.mini-table .tar {
  text-align: right;
}

.chan {
  color: var(--ink-1);
  font-weight: 550;
  margin-right: 6px;
}

.mini-table td .mono.dim {
  font-size: var(--fs-2xs);
}

.mini-table td code {
  font-size: var(--fs-xs);
  color: var(--ink-1);
}

.is-hot {
  color: var(--danger) !important;
  font-weight: 600;
}

/* ---- counts ---- */
.counts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--sp-4) var(--sp-5);
}

.count {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.count__label {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.count__value {
  font-size: var(--fs-lg);
  font-weight: 600;
  color: var(--ink-1);
}

.count__value.accent {
  color: var(--accent);
}

@media (max-width: 1400px) {
  .grid--kpi {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 1180px) {
  .split {
    grid-template-columns: 1fr;
  }
  .grid--kpi {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
