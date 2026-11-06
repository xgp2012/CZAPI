<template>
  <div class="cz-page">
    <div class="cz-toolbar">
      <el-input
        v-model="keyword"
        placeholder="按 Key 名称搜索"
        style="width: 240px"
        clearable
        @keyup.enter="search"
        @clear="search"
      />
      <el-button type="primary" @click="search">搜索</el-button>
      <el-button @click="load">刷新</el-button>
    </div>

    <el-table :data="items" v-loading="loading" border stripe>
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column prop="name" label="名称" min-width="160" />
      <el-table-column prop="username" label="所属客户" min-width="150" />
      <el-table-column prop="client_id" label="客户 ID" width="110" />
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'danger'" size="small">
            {{ row.status === 1 ? '正常' : '已禁用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="额度" width="120">
        <template #default="{ row }">
          <el-tag v-if="row.unlimited_quota" size="small" type="info">不限</el-tag>
          <span v-else>受限</span>
        </template>
      </el-table-column>
      <el-table-column label="已用（元）" width="120" align="right">
        <template #default="{ row }">{{ quotaToYuan(row.used_quota) }}</template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_time) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="120" fixed="right">
        <template #default="{ row }">
          <el-button
            size="small"
            type="danger"
            :disabled="row.status !== 1"
            @click="revoke(row)"
          >
            吊销
          </el-button>
        </template>
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
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from '../api'
import { fmtTime, quotaToYuan as quotaToYuanRaw } from '../format'

const items = ref([])
const loading = ref(false)
const keyword = ref('')
const page = ref(1)
const pageSize = 20
const total = ref(0)
const perUnit = ref(500000)

const quotaToYuan = (q) => quotaToYuanRaw(q, perUnit.value)

async function load() {
  loading.value = true
  try {
    const data = await http.get('/keys', {
      params: { page: page.value, page_size: pageSize, keyword: keyword.value },
    })
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

async function revoke(row) {
  try {
    await ElMessageBox.confirm(
      `吊销后该 Key 立即无法调用。历史用量记录会保留。确定吊销「${row.name}」？`,
      '吊销 Key',
      { type: 'warning' }
    )
  } catch {
    return
  }
  await http.post(`/keys/${row.id}/revoke`)
  ElMessage.success('已吊销')
  await load()
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
