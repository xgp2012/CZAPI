<script setup lang="ts">
/**
 * 用量明细 — date range, model filter, summary, paginated log table, CSV export.
 * Never shows prompt/response bodies: the contract does not return them.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import dayjs from 'dayjs'
import { consoleApi } from '@/api/console'
import type { ConsoleModel, UsageLog, UsageSummary } from '@/types/api'
import { downloadText, fmtDateTime, fmtInt, fmtMoney, fmtMs, fmtPercent, fmtTokens } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatCard from '@/components/StatCard.vue'
import Icon from '@/components/Icon.vue'

const range = ref<[string, string]>([
  dayjs().subtract(6, 'day').format('YYYY-MM-DD'),
  dayjs().format('YYYY-MM-DD'),
])
const model = ref('')
const page = ref(1)
const pageSize = ref(20)

const logs = ref<UsageLog[]>([])
const total = ref(0)
const summary = ref<UsageSummary | null>(null)
const models = ref<ConsoleModel[]>([])
const loading = ref(false)
const summaryLoading = ref(false)
const exporting = ref(false)
const error = ref<string | null>(null)

const fromUnix = computed(() => (range.value?.[0] ? dayjs(range.value[0]).startOf('day').unix() : 0))
const toUnix = computed(() => (range.value?.[1] ? dayjs(range.value[1]).endOf('day').unix() : Math.floor(Date.now() / 1000)))

async function loadLogs() {
  loading.value = true
  error.value = null
  try {
    const res = await consoleApi.usageLogs({
      from: fromUnix.value,
      to: toUnix.value,
      model: model.value || undefined,
      page: page.value,
      page_size: pageSize.value,
    })
    logs.value = res.items
    total.value = res.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载用量失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

async function loadSummary() {
  summaryLoading.value = true
  try {
    summary.value = await consoleApi.usageSummary(fromUnix.value, toUnix.value, 'day')
  } catch {
    /* the table still answers the question; the summary is an extra */
  } finally {
    summaryLoading.value = false
  }
}

async function loadAll() {
  page.value = 1
  await Promise.all([loadLogs(), loadSummary()])
}

onMounted(async () => {
  try {
    const res = await consoleApi.models()
    models.value = res.items
  } catch {
    /* filter degrades to "all models" */
  }
  await loadAll()
})

function onPage(p: number) {
  page.value = p
  loadLogs()
}

function onSize(s: number) {
  pageSize.value = s
  page.value = 1
  loadLogs()
}

async function exportCsv() {
  exporting.value = true
  try {
    const csv = await consoleApi.usageExport({ from: fromUnix.value, to: toUnix.value, model: model.value || undefined })
    downloadText(`czapi-usage-${range.value[0]}_${range.value[1]}.csv`, csv, 'text/csv;charset=utf-8')
    ElMessage.success('CSV 已开始下载')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '导出失败')
  } finally {
    exporting.value = false
  }
}

const totals = computed(() => summary.value?.totals ?? { requests: 0, prompt_tokens: 0, completion_tokens: 0, cost_micros: 0 })
const successRate = computed(() => {
  if (!logs.value.length) return null
  const ok = logs.value.filter((l) => l.status_code === 200).length
  return ok / logs.value.length
})

function billingLabel(m: string) {
  return { subscription: '套餐', balance: '余额', hybrid: '套餐+余额', free: '免费' }[m] ?? m
}
</script>

<template>
  <div class="page">
    <PageHeader title="用量明细" desc="按日期与模型筛选调用记录。为满足合规要求，明细不包含 prompt 与 response 正文。">
      <template #actions>
        <el-button :loading="exporting" @click="exportCsv">
          <Icon name="download" :size="15" /><span class="btn-gap">导出 CSV</span>
        </el-button>
        <el-button type="primary" :loading="loading || summaryLoading" @click="loadAll">
          <Icon name="search" :size="15" /><span class="btn-gap">查询</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-date-picker v-model="range" type="daterange" range-separator="→" start-placeholder="开始日期" end-placeholder="结束日期"
        value-format="YYYY-MM-DD" :clearable="false" style="width: 268px" @change="loadAll" />
      <el-select v-model="model" placeholder="全部模型" clearable style="width: 200px" @change="loadAll">
        <el-option v-for="m in models" :key="m.model_name" :label="m.display_name" :value="m.model_name" />
      </el-select>
      <span class="toolbar__spacer" />
      <span class="toolbar__note mono">{{ range[0] }} → {{ range[1] }}</span>
    </div>

    <!-- summary -->
    <div class="grid grid--stats">
      <StatCard v-if="summaryLoading" label="请求数" value="—" />
      <StatCard v-else label="请求数" :value="fmtInt(totals.requests)" :hint="`成功请求的计入统计`" />
      <StatCard label="消费" :value="fmtMoney(summary?.totals.cost_micros)" tone="accent" :hint="'按计费引擎结算的金额'" />
      <StatCard label="Token" :value="fmtTokens(totals.prompt_tokens + totals.completion_tokens)" :hint="`输入 ${fmtTokens(totals.prompt_tokens)} · 输出 ${fmtTokens(totals.completion_tokens)}`" />
      <StatCard label="本页成功率" :value="successRate === null ? '—' : fmtPercent(successRate, 1)" :tone="successRate !== null && successRate < 0.98 ? 'danger' : 'ok'" :hint="`当前页 ${logs.length} 条记录`" />
    </div>

    <!-- table -->
    <section v-if="loading && !logs.length" class="panel">
      <div class="panel__body">
        <div v-for="i in 6" :key="i" class="row-skeleton">
          <span class="sk" style="width: 16%; height: 12px" />
          <span class="sk" style="width: 10%; height: 12px" />
          <span class="sk" style="width: 14%; height: 12px" />
          <span class="sk" style="width: 8%; height: 12px" />
          <span class="sk" style="width: 12%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error && !logs.length" class="panel">
      <EmptyState icon="alert" title="用量加载失败" :desc="error">
        <el-button size="small" @click="loadAll">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!logs.length" class="panel">
      <EmptyState icon="chart" title="这个时间段没有调用记录" desc="换一个日期范围，或确认这个模型最近是否被调用过。">
        <el-button size="small" @click="range = [dayjs().subtract(29, 'day').format('YYYY-MM-DD'), dayjs().format('YYYY-MM-DD')]; loadAll()">
          查看近 30 天
        </el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="logs" row-key="request_id" size="default">
        <el-table-column label="时间" width="140">
          <template #default="{ row }"><span class="mono cell-time">{{ fmtDateTime(row.created_at) }}</span></template>
        </el-table-column>

        <el-table-column label="模型" min-width="170">
          <template #default="{ row }">
            <code class="cell-model">{{ row.model }}</code>
          </template>
        </el-table-column>

        <el-table-column label="输入 / 输出" width="150" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono">{{ fmtInt(row.prompt_tokens) }}</span>
            <span class="dim"> / </span>
            <span class="mono">{{ fmtInt(row.completion_tokens) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="费用" width="112" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono cell-cost">{{ row.cost_micros ? fmtMoney(row.cost_micros, 4) : '—' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="计费" width="112">
          <template #default="{ row }">
            <span class="bill-tag" :class="{ 'bill-tag--free': row.billing_mode === 'free' }">{{ billingLabel(row.billing_mode) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="模式" width="88">
          <template #default="{ row }">
            <span class="mono dim">{{ row.stream ? 'stream' : 'sync' }}</span>
            <span v-if="row.estimated" class="est" title="token 为启发式估算">估</span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="86">
          <template #default="{ row }">
            <span class="badge" :class="row.status_code === 200 ? 'badge--ok' : 'badge--danger'">{{ row.status_code }}</span>
          </template>
        </el-table-column>

        <el-table-column label="TTFT / 总耗时" width="140" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono">{{ fmtMs(row.ttft_ms) }}</span>
            <span class="dim"> · </span>
            <span class="mono dim">{{ fmtMs(row.latency_ms) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="Key / Host" width="130">
          <template #default="{ row }">
            <span class="mono dim cell-id">key#{{ row.api_key_id }} · host#{{ row.v10_host_id }}</span>
          </template>
        </el-table-column>

        <el-table-column label="请求 ID" min-width="150">
          <template #default="{ row }">
            <code class="cell-req" :title="row.request_id">{{ row.request_id }}</code>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination :current-page="page" :page-size="pageSize" :total="total" :page-sizes="[20, 50, 100, 200]"
        layout="total, sizes, prev, pager, next" background @current-change="onPage" @size-change="onSize" />
    </section>
  </div>
</template>

<style scoped>
.btn-gap {
  margin-left: 6px;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  flex-wrap: wrap;
}

.toolbar__spacer {
  flex: 1;
}

.toolbar__note {
  font-size: var(--fs-xs);
  color: var(--ink-4);
}

.grid--stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--sp-4);
}

.row-skeleton {
  display: flex;
  gap: var(--sp-6);
  padding: 12px 0;
  border-bottom: 1px solid var(--line-1);
}

.cell-time {
  font-size: var(--fs-xs);
  color: var(--ink-2);
}

.cell-model {
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  color: var(--ink-1);
}

.cell-cost {
  color: var(--ink-1);
  font-weight: 550;
}

.cell-id {
  font-size: var(--fs-2xs);
}

.cell-req {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bill-tag {
  font-size: var(--fs-2xs);
  padding: 1px 6px;
  border-radius: var(--r-xs);
  background: var(--bg-inset);
  border: 1px solid var(--line-1);
  color: var(--ink-2);
  white-space: nowrap;
}

.bill-tag--free {
  color: var(--ink-4);
}

.est {
  margin-left: 4px;
  font-size: var(--fs-2xs);
  padding: 0 3px;
  border-radius: 2px;
  border: 1px solid color-mix(in srgb, var(--warn) 34%, transparent);
  color: var(--warn);
}

@media (max-width: 1280px) {
  .grid--stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
