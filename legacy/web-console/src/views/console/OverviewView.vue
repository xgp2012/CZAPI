<script setup lang="ts">
/**
 * 概览 — the user centre landing screen.
 *
 * Layout logic: the top strip answers "can I keep spending?" (balance + today +
 * month), then each subscription gets a card whose quota meter reads at a
 * glance, then the 30-day trend. Recharge is a jump to V10, never an in-app
 * payment flow, so the button is explicit about where money changes hands.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useOverviewStore } from '@/stores/overview'
import type { Overview, SubscriptionSummary } from '@/types/api'
import { daysLeft, fmtCompact, fmtDate, fmtDateTime, fmtMoney, fmtMoneyLong, fmtTokens, ratio } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import StatCard from '@/components/StatCard.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import LineChart, { type LineSeries } from '@/components/LineChart.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const store = useOverviewStore()
const data = ref<Overview | null>(null)
const error = ref<string | null>(null)

const loading = computed(() => store.loading && !data.value)
const failed = computed(() => Boolean(error.value))

async function load() {
  error.value = null
  try {
    data.value = await store.refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
    ElMessage.error(error.value)
  }
}

onMounted(load)

const subs = computed<SubscriptionSummary[]>(() => data.value?.subscriptions ?? [])

const categories = computed(() => (data.value?.trend ?? []).map((p) => p.day.slice(5)))

const series = computed<LineSeries[]>(() => {
  const trend = data.value?.trend ?? []
  return [
    { name: '请求数', data: trend.map((p) => p.requests), tone: 'steel', axis: 'left', bar: true },
    { name: '消费（元）', data: trend.map((p) => Math.round((p.cost_micros / 1_000_000) * 100) / 100), tone: 'accent', axis: 'right' },
  ]
})

const todayTokens = computed(() => {
  const t = data.value?.today
  return t ? t.prompt_tokens + t.completion_tokens : 0
})

const monthTokens = computed(() => {
  const m = data.value?.month
  return m ? m.prompt_tokens + m.completion_tokens : 0
})

const nextExpiry = computed(() => {
  const active = subs.value.filter((s) => s.status === 'active')
  if (!active.length) return null
  return active.reduce((a, b) => (a.period_end < b.period_end ? a : b))
})

/** How much of the month's spend went through the quota bucket vs balance. */
const quotaShare = computed(() => {
  const total = subs.value.reduce((a, s) => a + s.quota_total, 0)
  const used = subs.value.reduce((a, s) => a + s.quota_used, 0)
  return { total, used, pct: ratio(used, total) }
})

function openRecharge() {
  const url = data.value?.recharge_url
  if (!url) {
    ElMessage.warning('后端未下发充值地址')
    return
  }
  window.open(url, '_blank', 'noopener,noreferrer')
}

const STATUS_LABEL: Record<SubscriptionSummary['status'], string> = {
  active: '生效中',
  suspended: '已暂停',
  expired: '已过期',
  pending: '待生效',
}

const BILLING_LABEL: Record<SubscriptionSummary['billing_mode'], string> = {
  subscription: '套餐内',
  balance: '余额',
  hybrid: '套餐+余额',
  free: '免费',
}

function statusLabel(s: SubscriptionSummary['status']) {
  return STATUS_LABEL[s] ?? s
}

function statusTone(s: SubscriptionSummary['status']) {
  return s === 'active' ? 'ok' : s === 'suspended' ? 'warn' : 'idle'
}

function billingLabel(mode: SubscriptionSummary['billing_mode']) {
  return BILLING_LABEL[mode] ?? mode
}
</script>

<template>
  <div class="page">
    <PageHeader title="概览" desc="套餐额度、余额与近 30 天的调用趋势。所有金额单位为元，额度单位为 token。">
      <template #actions>
        <el-button size="default" :loading="store.loading" @click="load">
          <Icon name="refresh" :size="15" />
          <span class="btn-gap">刷新</span>
        </el-button>
        <el-button type="primary" size="default" @click="openRecharge">
          <Icon name="money" :size="15" />
          <span class="btn-gap">充值</span>
        </el-button>
      </template>
    </PageHeader>

    <div v-if="loading" class="grid grid--stats">
      <div v-for="i in 4" :key="i" class="stat-skeleton">
        <span class="sk" style="width: 56px; height: 11px" />
        <span class="sk" style="width: 118px; height: 26px; margin-top: 10px" />
        <span class="sk" style="width: 74px; height: 10px; margin-top: 10px" />
      </div>
    </div>

    <div v-else-if="failed" class="panel">
      <EmptyState icon="alert" title="概览加载失败" :desc="error ?? undefined">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </div>

    <template v-else-if="data">
      <!-- ---- key numbers ---- -->
      <div class="grid grid--stats">
        <StatCard label="账户余额" :value="fmtMoney(data.balance_micros)" tone="accent"
          :hint="data.balance_micros > 0 ? '余额不足时自动回落到套餐扣减' : '余额为 0，超量调用将被拒绝'" />
        <StatCard label="今日消费" :value="fmtMoney(data.today.cost_micros)" :hint="`${data.today.requests} 次请求 · ${fmtTokens(todayTokens)} token`" />
        <StatCard label="本月消费" :value="fmtMoney(data.month.cost_micros)" :hint="`${data.month.requests} 次请求 · ${fmtTokens(monthTokens)} token`" />
        <StatCard label="本月请求" :value="fmtCompact(data.month.requests)" :hint="nextExpiry ? `最晚到期 ${fmtDate(nextExpiry.period_end)}` : '暂无生效中的套餐'" />
      </div>

      <!-- ---- subscriptions ---- -->
      <section class="panel">
        <div class="panel__head">
          <div class="flex-between" style="flex: 1">
            <span class="panel__title">套餐额度</span>
            <span class="panel__hint">
              合计已用
              <b class="mono">{{ fmtTokens(quotaShare.used) }}</b>
              /
              <span class="mono">{{ fmtTokens(quotaShare.total) }}</span>
              （{{ (quotaShare.pct * 100).toFixed(1) }}%）
            </span>
          </div>
        </div>

        <div v-if="!subs.length" class="panel__body">
          <EmptyState compact icon="shield" title="还没有生效中的套餐" desc="在 V10 下单后，订阅会自动同步到这里；也可以在 Key 管理里直接使用余额计费。" />
        </div>

        <div v-else class="sub-grid">
          <article v-for="s in subs" :key="s.subscription_id" class="subcard">
            <header class="subcard__head">
              <div class="subcard__title">
                <h3>{{ s.plan_name }}</h3>
                <span class="mono subcard__code">{{ s.plan_code }}</span>
              </div>
              <span class="badge" :class="`badge--${statusTone(s.status)}`">{{ statusLabel(s.status) }}</span>
            </header>

            <dl class="subcard__meta">
              <div>
                <dt>周期</dt>
                <dd class="mono">{{ fmtDate(s.period_start) }} → {{ fmtDate(s.period_end) }}</dd>
              </div>
              <div>
                <dt>剩余</dt>
                <dd class="mono subcard__remain" :class="{ 'is-low': daysLeft(s.period_end) !== null && (daysLeft(s.period_end) ?? 0) <= 5 }">
                  {{ daysLeft(s.period_end) }} 天
                </dd>
              </div>
              <div>
                <dt>计费</dt>
                <dd>{{ billingLabel(s.billing_mode) }}</dd>
              </div>
              <div>
                <dt>主机</dt>
                <dd class="mono">#{{ s.v10_host_id }}</dd>
              </div>
            </dl>

            <div class="subcard__quota">
              <QuotaMeter :used="s.quota_used" :total="s.quota_total" :height="9" show-labels />
              <div class="subcard__left">
                剩余 <b class="mono">{{ fmtTokens(s.quota_remaining) }}</b> token
              </div>
            </div>
          </article>
        </div>
      </section>

      <!-- ---- trend ---- -->
      <section class="panel">
        <div class="panel__head">
          <div class="flex-between" style="flex: 1">
            <span class="panel__title">近 30 天趋势</span>
            <span class="panel__hint mono">{{ data.trend[0]?.day }} → {{ data.trend[data.trend.length - 1]?.day }}</span>
          </div>
        </div>
        <div class="panel__body panel__body--chart">
          <LineChart :categories="categories" :series="series" :height="286" y-name="请求数" y-name-right="消费 / 元" />
        </div>
      </section>

      <!-- ---- recharge note ---- -->
      <section class="recharge">
        <div class="recharge__body">
          <span class="recharge__icon"><Icon name="money" :size="18" /></span>
          <div>
            <h3>充值在 V10 完成</h3>
            <p>
              本网关不收款。点击「充值」会跳转到 V10 的充值页，支付成功后再由插件回调网关入账；
              卡密兑换同样在 V10 插件页完成。入账记录可在管理后台「财务流水」中看到。
            </p>
          </div>
        </div>
        <div class="recharge__aside">
          <span class="recharge__url mono">{{ data.recharge_url }}</span>
          <el-button size="small" @click="openRecharge">打开充值页</el-button>
        </div>
      </section>

      <p class="footnote">
        数据更新于 {{ fmtDateTime(Math.floor(Date.now() / 1000)) }} · 金额按微元存储（1 元 = 1,000,000 微元），此处已换算为元。
        当前余额 <span class="mono">{{ fmtMoneyLong(data.balance_micros) }}</span>。
      </p>
    </template>
  </div>
</template>

<style scoped>
.grid--stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--sp-4);
}

.stat-skeleton {
  display: flex;
  flex-direction: column;
  padding: var(--sp-4) var(--sp-5);
  background: var(--bg-panel);
  border: 1px solid var(--line-1);
  border-radius: var(--r-md);
}

.btn-gap {
  margin-left: 6px;
}

/* ---- subscription cards ---- */
.sub-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 0;
}

.subcard {
  display: flex;
  flex-direction: column;
  gap: var(--sp-4);
  padding: var(--sp-5);
  border-right: 1px solid var(--line-1);
  border-bottom: 1px solid var(--line-1);
  background: var(--bg-panel);
}

.subcard__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--sp-3);
}

.subcard__title {
  display: flex;
  align-items: baseline;
  gap: var(--sp-2);
}

.subcard__title h3 {
  font-size: var(--fs-md);
  font-weight: 600;
}

.subcard__code {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.subcard__meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--sp-2) var(--sp-4);
  margin: 0;
}

.subcard__meta dt {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.subcard__meta dd {
  margin: 1px 0 0;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}

.subcard__remain.is-low {
  color: var(--warn);
  font-weight: 600;
}

.subcard__quota {
  display: flex;
  flex-direction: column;
  gap: 7px;
}

.subcard__left {
  font-size: var(--fs-2xs);
  color: var(--ink-3);
}

.subcard__left b {
  color: var(--ink-1);
  font-weight: 600;
}

.panel__body--chart {
  padding: var(--sp-4) var(--sp-4) var(--sp-2);
}

/* ---- recharge strip ---- */
.recharge {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-6);
  padding: var(--sp-5);
  border: 1px solid var(--line-1);
  border-left: 2px solid var(--accent);
  border-radius: var(--r-md);
  background: var(--bg-panel);
  flex-wrap: wrap;
}

.recharge__body {
  display: flex;
  gap: var(--sp-4);
  align-items: flex-start;
  max-width: 72ch;
}

.recharge__icon {
  display: grid;
  place-items: center;
  width: 34px;
  height: 34px;
  flex: 0 0 34px;
  border-radius: var(--r-sm);
  background: var(--accent-soft);
  color: var(--accent);
}

.recharge h3 {
  font-size: var(--fs-sm);
  font-weight: 600;
  margin-bottom: 3px;
}

.recharge p {
  font-size: var(--fs-xs);
  color: var(--ink-3);
  line-height: var(--lh-normal);
}

.recharge__aside {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
}

.recharge__url {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
