<template>
  <div class="cz-page">
    <div class="cz-toolbar">
      <el-select v-model="filters.kind" placeholder="来源" clearable style="width: 150px">
        <el-option label="全部" value="" />
        <el-option label="订单" value="v10_order" />
        <el-option label="卡密" value="redeem_code" />
        <el-option label="手工调额" value="admin_adjust" />
        <el-option label="退款" value="refund" />
      </el-select>
      <el-input
        v-model="filters.keyword"
        placeholder="按 ref_id 搜索"
        style="width: 220px"
        clearable
        @keyup.enter="search"
        @clear="search"
      />
      <el-date-picker
        v-model="filters.range"
        type="datetimerange"
        start-placeholder="开始"
        end-placeholder="结束"
        style="width: 360px"
      />
      <el-button type="primary" @click="search">查询</el-button>
      <el-button @click="reset">重置</el-button>
    </div>

    <el-table :data="items" v-loading="loading" border stripe>
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column prop="ref_type" label="凭证类型" width="130" />
      <el-table-column prop="ref_id" label="凭证 ID" min-width="200" show-overflow-tooltip>
        <template #default="{ row }">
          <span class="cz-mono">{{ row.ref_id }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="kind" label="来源" width="120" />
      <el-table-column label="变动（元）" width="130" align="right">
        <template #default="{ row }">
          <span :style="{ color: row.delta_quota >= 0 ? '#67c23a' : '#f56c6c' }">
            {{ row.delta_quota >= 0 ? '+' : '' }}{{ quotaToYuan(row.delta_quota) }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="变动后余额（元）" width="150" align="right">
        <template #default="{ row }">{{ quotaToYuan(row.balance_after) }}</template>
      </el-table-column>
      <el-table-column prop="remark" label="备注" min-width="180" show-overflow-tooltip />
      <el-table-column label="时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
    </el-table>

    <div class="cz-pager">
      <el-pagination
        v-model:current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        @current-change="load"
      />
    </div>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import http from '../api'
import { fmtTime, quotaToYuan as quotaToYuanRaw } from '../format'

const items = ref([])
const loading = ref(false)
const page = ref(1)
const pageSize = 20
const total = ref(0)
const perUnit = ref(500000)

const filters = reactive({ kind: '', keyword: '', range: null })

const quotaToYuan = (q) => quotaToYuanRaw(q, perUnit.value)

async function load() {
  loading.value = true
  try {
    const params = {
      page: page.value,
      page_size: pageSize,
      kind: filters.kind || undefined,
      keyword: filters.keyword || undefined,
    }
    if (filters.range && filters.range.length === 2) {
      params.from = Math.floor(new Date(filters.range[0]).getTime() / 1000)
      params.to = Math.floor(new Date(filters.range[1]).getTime() / 1000)
    }
    const data = await http.get('/ledger', { params })
    items.value = data.items || []
    total.value = data.total || 0
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 1
  load()
}

function reset() {
  filters.kind = ''
  filters.keyword = ''
  filters.range = null
  search()
}

onMounted(async () => {
  try {
    const ov = await http.get('/overview')
    perUnit.value = Number(ov.quota_per_unit) || 500000
  } catch {
    // 默认比例
  }
  load()
})
</script>
