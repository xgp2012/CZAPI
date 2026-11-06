<script setup lang="ts">
/**
 * 财务流水 — credit_ledger.
 *
 * Reads as a statement: newest first, amount coloured by direction, and the
 * running balance (`balance_after`) in its own mono column so a gap in the
 * books is visible by scanning one column. The gateway never takes payments;
 * every credit here traces back to a V10 voucher.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { LedgerEntry } from '@/types/api'
import { fmtDateTime, fmtInt, fmtMoneyLong } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const rows = ref<LedgerEntry[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const type = ref('')
const accountId = ref<number | undefined>(undefined)
const loading = ref(false)
const error = ref<string | null>(null)

const TYPE_LABEL: Record<string, string> = {
  recharge: '充值',
  consume: '消费',
  refund: '退款',
  admin_adjust: '管理调账',
  redeem_code: '卡密兑换',
  v10_order: 'V10 订单',
  expire: '过期作废',
}

const types = Object.keys(TYPE_LABEL)

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await adminApi.listLedger({
      page: page.value,
      page_size: pageSize.value,
      type: type.value || undefined,
      account_id: accountId.value,
    })
    rows.value = res.items
    total.value = res.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载流水失败'
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

/** Net balance change on this page — a quick sanity check while filtering. */
const netChange = computed(() => rows.value.reduce((a, r) => a + r.amount_micros, 0))
const credits = computed(() => rows.value.filter((r) => r.amount_micros > 0).length)
const debits = computed(() => rows.value.filter((r) => r.amount_micros < 0).length)

const typeTone = (t: string) => (t === 'consume' ? 'idle' : t === 'refund' || t === 'expire' ? 'warn' : 'ok')
</script>

<template>
  <div class="page">
    <PageHeader title="财务流水" desc="每一条都可能来自 V10 的订单、卡密、退款或管理员调账。右侧「余额」是这条记录发生后的账户余额。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-select v-model="type" placeholder="全部类型" clearable style="width: 168px" @change="applyFilter">
        <el-option v-for="t in types" :key="t" :label="TYPE_LABEL[t]" :value="t" />
      </el-select>
      <el-input-number v-model="accountId" :min="1" :controls="false" placeholder="account_id" style="width: 150px"
        @change="applyFilter" />
      <span class="toolbar__spacer" />
      <div class="summary">
        <span>本页净变动 <b class="mono" :class="netChange >= 0 ? 'pos' : 'neg'">{{ fmtMoneyLong(netChange) }}</b></span>
        <span class="dim">入账 {{ credits }} · 出账 {{ debits }}</span>
      </div>
    </div>

    <section v-if="loading && !rows.length" class="panel">
      <div class="panel__body">
        <div v-for="i in 8" :key="i" class="row-skeleton">
          <span class="sk" style="width: 14%; height: 12px" />
          <span class="sk" style="width: 10%; height: 12px" />
          <span class="sk" style="width: 20%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error && !rows.length" class="panel">
      <EmptyState icon="alert" title="流水加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!rows.length" class="panel">
      <EmptyState icon="receipt" title="没有符合条件的流水" desc="换一个类型或账户 ID，或清空筛选查看全部。">
        <el-button size="small" @click="type = ''; accountId = undefined; applyFilter()">清空筛选</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="rows" row-key="id">
        <el-table-column label="时间" width="140">
          <template #default="{ row }"><span class="mono cell-time">{{ fmtDateTime(row.created_at) }}</span></template>
        </el-table-column>

        <el-table-column label="账户" width="112">
          <template #default="{ row }"><span class="mono">#{{ row.account_id }}</span></template>
        </el-table-column>

        <el-table-column label="类型" width="118">
          <template #default="{ row }">
            <span class="badge" :class="`badge--${typeTone(row.type)}`">{{ TYPE_LABEL[row.type] ?? row.type }}</span>
          </template>
        </el-table-column>

        <el-table-column label="金额" width="150" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono amount" :class="row.amount_micros >= 0 ? 'amount--in' : 'amount--out'">
              {{ row.amount_micros >= 0 ? '+' : '' }}{{ fmtMoneyLong(row.amount_micros) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column label="余额（发生后）" width="158" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono balance" :class="{ 'is-negative': row.balance_after < 0 }">{{ fmtMoneyLong(row.balance_after) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="凭证" min-width="196">
          <template #default="{ row }">
            <span class="voucher mono" :title="`${row.ref_type} / ${row.ref_id}`">{{ row.ref_type }} · {{ row.ref_id }}</span>
          </template>
        </el-table-column>

        <el-table-column label="备注" min-width="230">
          <template #default="{ row }"><span class="remark">{{ row.remark || '—' }}</span></template>
        </el-table-column>

        <el-table-column label="ID" width="88" align="right" class-name="col-num">
          <template #default="{ row }"><span class="mono dim">{{ fmtInt(row.id) }}</span></template>
        </el-table-column>
      </el-table>

      <el-pagination :current-page="page" :page-size="pageSize" :total="total" :page-sizes="[20, 50, 100]"
        layout="total, sizes, prev, pager, next" background
        @current-change="(p: number) => { page = p; load() }" @size-change="(s: number) => { pageSize = s; page = 1; load() }" />
    </section>

    <p class="footnote">
      同一 <code class="mono">(ref_type, ref_id)</code> 只会产生一条记录 —— 这是防重复入账的核心约束。
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

.summary {
  display: flex;
  align-items: baseline;
  gap: var(--sp-4);
  font-size: var(--fs-xs);
  color: var(--ink-3);
}

.pos {
  color: var(--ok);
}

.neg {
  color: var(--danger);
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

.amount {
  font-weight: 600;
}

.amount--in {
  color: var(--ok);
}

.amount--out {
  color: var(--ink-2);
}

.balance {
  color: var(--ink-1);
}

.balance.is-negative {
  color: var(--danger);
}

.voucher {
  font-size: var(--fs-2xs);
  color: var(--ink-3);
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.remark {
  font-size: var(--fs-xs);
  color: var(--ink-2);
}

.footnote {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}
</style>
