<template>
  <div class="cz-page">
    <div class="cz-toolbar">
      <el-select v-model="status" placeholder="状态" clearable style="width: 160px">
        <el-option label="全部" value="" />
        <el-option label="active（正常）" value="active" />
        <el-option label="suspended（暂停）" value="suspended" />
        <el-option label="terminated（终止）" value="terminated" />
      </el-select>
      <el-input
        v-model="keyword"
        placeholder="按 host_id 或客户 ID 搜索"
        style="width: 240px"
        clearable
        @keyup.enter="search"
        @clear="search"
      />
      <el-button type="primary" @click="search">查询</el-button>
      <el-button @click="load">刷新</el-button>
    </div>

    <el-table :data="items" v-loading="loading" border stripe>
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column prop="host_id" label="Host ID" width="110" />
      <el-table-column prop="client_id" label="客户 ID" width="110" />
      <el-table-column prop="plan_id" label="套餐 ID" width="100" />
      <el-table-column label="状态" width="120">
        <template #default="{ row }">
          <el-tag :type="statusType(row.host_status)" size="small">
            {{ row.host_status }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="user_subscription_id" label="订阅 ID" width="110" />
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="190" fixed="right">
        <template #default="{ row }">
          <el-button
            v-if="row.host_status === 'active'"
            size="small"
            type="warning"
            @click="setStatus(row, 'suspended')"
          >
            暂停
          </el-button>
          <el-button
            v-else-if="row.host_status === 'suspended'"
            size="small"
            type="success"
            @click="setStatus(row, 'active')"
          >
            恢复
          </el-button>
          <el-button
            v-if="row.host_status !== 'terminated'"
            size="small"
            type="danger"
            @click="setStatus(row, 'terminated')"
          >
            终止
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
import { fmtTime } from '../format'

const items = ref([])
const loading = ref(false)
const status = ref('')
const keyword = ref('')
const page = ref(1)
const pageSize = 20
const total = ref(0)

function statusType(s) {
  if (s === 'active') return 'success'
  if (s === 'suspended') return 'warning'
  return 'info'
}

async function load() {
  loading.value = true
  try {
    const data = await http.get('/hosts', {
      params: {
        page: page.value,
        page_size: pageSize,
        status: status.value || undefined,
        keyword: keyword.value || undefined,
      },
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

async function setStatus(row, next) {
  const labels = { active: '恢复', suspended: '暂停', terminated: '终止' }
  const hints = {
    active: '恢复后连带启用的 Key 会重新可用（已吊销的不会复活）。',
    suspended: '暂停会连带停用该客户全部 Key，用于欠费处理。',
    terminated: '终止不可撤销，订阅结束且 Key 永久失效。',
  }
  try {
    await ElMessageBox.confirm(hints[next], `${labels[next]} host ${row.host_id}`, {
      type: 'warning',
    })
  } catch {
    return
  }
  await http.post(`/hosts/${row.id}/status`, { status: next, reason: 'console' })
  ElMessage.success('已更新')
  await load()
}

onMounted(load)
</script>
