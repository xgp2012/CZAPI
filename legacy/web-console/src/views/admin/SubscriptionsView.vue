<script setup lang="ts">
/**
 * 订阅 — every subscription the gateway knows about, with the three manual
 * operations the contract allows: suspend / resume / reset-quota.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { AdminSubscription } from '@/types/api'
import { fmtDateTime, fmtPercent, daysLeft } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import Icon from '@/components/Icon.vue'

const rows = ref<AdminSubscription[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const status = ref<'' | 'active' | 'suspended' | 'expired' | 'pending'>('')
const loading = ref(false)
const error = ref<string | null>(null)
const busyId = ref<number | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await adminApi.listSubscriptions(status.value, page.value, pageSize)
    rows.value = res.items
    total.value = res.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载订阅失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)

function applyFilter() {
  page.value = 1
  load()
}

async function act(row: AdminSubscription, action: 'suspend' | 'resume' | 'reset-quota') {
  const copy = {
    suspend: {
      title: '手工暂停订阅',
      body: `暂停后订阅下所有 Key 立即无法调用（额度冻结，不释放）。「${row.plan_name}」#${row.subscription_id}。`,
      confirm: '暂停',
      success: '订阅已暂停',
    },
    resume: {
      title: '手工恢复订阅',
      body: `恢复「${row.plan_name}」#${row.subscription_id}后，其下 Key 立即恢复调用。`,
      confirm: '恢复',
      success: '订阅已恢复',
    },
    'reset-quota': {
      title: '重置周期额度',
      body: `将「${row.plan_name}」#${row.subscription_id} 的已用额度归零（当前 ${fmtPercent(row.quota_used / (row.quota_total || 1))} 已用）。此操作会直接增加可用额度，请确认已核对账单。`,
      confirm: '重置',
      success: '额度已重置',
    },
  }[action]

  try {
    await ElMessageBox.confirm(copy.body, copy.title, {
      confirmButtonText: copy.confirm,
      cancelButtonText: '取消',
      type: action === 'reset-quota' ? 'warning' : 'info',
    })
  } catch {
    return
  }

  busyId.value = row.subscription_id
  try {
    await adminApi.subscriptionAction(row.subscription_id, action)
    ElMessage.success(copy.success)
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    busyId.value = null
  }
}

const statusTone = (s: string) => (s === 'active' ? 'ok' : s === 'suspended' ? 'warn' : s === 'expired' ? 'danger' : 'idle')

const counts = computed(() => ({
  all: total.value,
  active: rows.value.filter((r) => r.status === 'active').length,
}))
</script>

<template>
  <div class="page">
    <PageHeader title="订阅" desc="订阅是额度的载体。暂停会冻结额度但不释放；重置额度会把本周期已用量归零，仅用于人工补偿。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-radio-group v-model="status" size="default" @change="applyFilter">
        <el-radio-button value="">全部</el-radio-button>
        <el-radio-button value="active">生效中</el-radio-button>
        <el-radio-button value="suspended">已暂停</el-radio-button>
        <el-radio-button value="expired">已过期</el-radio-button>
        <el-radio-button value="pending">待生效</el-radio-button>
      </el-radio-group>
      <span class="toolbar__spacer" />
      <span class="toolbar__note">共 <b class="mono">{{ total }}</b> 条订阅</span>
    </div>

    <section v-if="loading && !rows.length" class="panel">
      <div class="panel__body">
        <div v-for="i in 6" :key="i" class="row-skeleton">
          <span class="sk" style="width: 18%; height: 12px" />
          <span class="sk" style="width: 22%; height: 12px" />
          <span class="sk" style="width: 16%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error && !rows.length" class="panel">
      <EmptyState icon="alert" title="订阅加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!rows.length" class="panel">
      <EmptyState icon="clock" title="没有匹配的订阅" desc="换一个状态筛选，或确认 V10 是否已经为该客户下单开通。">
        <el-button size="small" @click="status = ''; applyFilter()">查看全部</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="rows" row-key="subscription_id">
        <el-table-column label="订阅" min-width="180">
          <template #default="{ row }">
            <div class="cell-sub">
              <span class="cell-sub__name">{{ row.plan_name }}</span>
              <span class="cell-sub__id mono">#{{ row.subscription_id }} · {{ row.plan_code }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="账户" min-width="200">
          <template #default="{ row }">
            <span class="mono">{{ row.v10_client_id }}</span>
            <span class="mono dim"> · account #{{ row.account_id }}</span>
          </template>
        </el-table-column>

        <el-table-column label="Host" width="96">
          <template #default="{ row }"><code class="mono">{{ row.v10_host_id }}</code></template>
        </el-table-column>

        <el-table-column label="额度使用" min-width="220">
          <template #default="{ row }">
            <QuotaMeter :used="row.quota_used" :total="row.quota_total" show-labels />
          </template>
        </el-table-column>

        <el-table-column label="周期" min-width="190">
          <template #default="{ row }">
            <span class="mono dim">{{ fmtDateTime(row.period_start) }} → {{ fmtDateTime(row.period_end) }}</span>
            <span class="remain mono" :class="{ 'is-low': (daysLeft(row.period_end) ?? 99) <= 5 }">
              剩 {{ daysLeft(row.period_end) }} 天
            </span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <span class="badge" :class="`badge--${statusTone(row.status)}`">{{ row.status }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="212" fixed="right" align="right">
          <template #default="{ row }">
            <div class="row-actions">
              <el-button v-if="row.status === 'active'" text size="small" :loading="busyId === row.subscription_id"
                @click="act(row, 'suspend')">暂停</el-button>
              <el-button v-else-if="row.status === 'suspended'" text size="small" :loading="busyId === row.subscription_id"
                @click="act(row, 'resume')">恢复</el-button>
              <el-button text size="small" :loading="busyId === row.subscription_id" @click="act(row, 'reset-quota')">
                重置额度
              </el-button>
            </div>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination :current-page="page" :page-size="pageSize" :total="total" layout="total, prev, pager, next" background
        @current-change="(p: number) => { page = p; load() }" />
    </section>

    <p class="footnote">
      提示：额度单位是 token，余额单位是元（存微元）。当前页 {{ counts.active }} / {{ rows.length }} 条为生效中。
    </p>
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

.row-skeleton {
  display: flex;
  gap: var(--sp-6);
  padding: 12px 0;
  border-bottom: 1px solid var(--line-1);
}

.cell-sub {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.cell-sub__name {
  color: var(--ink-1);
  font-weight: 550;
}

.cell-sub__id {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.remain {
  display: block;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  margin-top: 2px;
}

.remain.is-low {
  color: var(--warn);
  font-weight: 600;
}

.footnote {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}
</style>
