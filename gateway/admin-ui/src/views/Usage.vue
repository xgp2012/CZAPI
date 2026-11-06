<template>
  <div class="cz-page">
    <div class="cz-toolbar">
      <el-date-picker
        v-model="range"
        type="datetimerange"
        start-placeholder="开始"
        end-placeholder="结束"
        style="width: 360px"
      />
      <el-radio-group v-model="tab">
        <el-radio-button value="customer">按客户</el-radio-button>
        <el-radio-button value="model">按模型</el-radio-button>
      </el-radio-group>
      <el-button type="primary" @click="load">查询</el-button>
    </div>

    <el-table v-if="tab === 'customer'" :data="rows" v-loading="loading" border stripe>
      <el-table-column prop="user_id" label="网关用户 ID" width="120" />
      <el-table-column prop="client_id" label="客户 ID" width="110" />
      <el-table-column prop="username" label="用户名" min-width="160" />
      <el-table-column label="消费（元）" width="140" align="right">
        <template #default="{ row }">
          <strong>{{ quotaToYuan(row.quota) }}</strong>
        </template>
      </el-table-column>
      <el-table-column prop="request_count" label="请求数" width="110" align="right" />
      <el-table-column prop="prompt_tokens" label="输入 Token" width="140" align="right" />
      <el-table-column prop="completion_tokens" label="输出 Token" width="140" align="right" />
    </el-table>

    <el-table v-else :data="rows" v-loading="loading" border stripe>
      <el-table-column prop="model_name" label="模型" min-width="220" />
      <el-table-column label="消费（元）" width="140" align="right">
        <template #default="{ row }">
          <strong>{{ quotaToYuan(row.quota) }}</strong>
        </template>
      </el-table-column>
      <el-table-column prop="request_count" label="请求数" width="110" align="right" />
      <el-table-column prop="prompt_tokens" label="输入 Token" width="140" align="right" />
      <el-table-column prop="completion_tokens" label="输出 Token" width="140" align="right" />
    </el-table>

    <el-empty v-if="!loading && rows.length === 0" description="该区间没有消费记录" />
  </div>
</template>

<script setup>
import { onMounted, ref, watch } from 'vue'
import http from '../api'
import { quotaToYuan as quotaToYuanRaw } from '../format'

const rows = ref([])
const loading = ref(false)
const tab = ref('customer')
const range = ref(null)
const perUnit = ref(500000)

const quotaToYuan = (q) => quotaToYuanRaw(q, perUnit.value)

async function load() {
  loading.value = true
  try {
    const params = {}
    if (range.value && range.value.length === 2) {
      params.from = Math.floor(new Date(range.value[0]).getTime() / 1000)
      params.to = Math.floor(new Date(range.value[1]).getTime() / 1000)
    }
    const url = tab.value === 'customer' ? '/usage/summary' : '/usage/models'
    const data = await http.get(url, { params })
    rows.value = data.items || []
  } finally {
    loading.value = false
  }
}

watch(tab, load)

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
