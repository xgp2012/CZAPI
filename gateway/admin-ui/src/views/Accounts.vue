<template>
  <div class="cz-page">
    <div class="cz-toolbar">
      <el-input
        v-model="keyword"
        placeholder="按 client_id 或用户名搜索"
        style="width: 260px"
        clearable
        @keyup.enter="search"
        @clear="search"
      />
      <el-button type="primary" @click="search">搜索</el-button>
      <el-button @click="load">刷新</el-button>
    </div>

    <el-table :data="items" v-loading="loading" border stripe>
      <el-table-column prop="user_id" label="网关用户 ID" width="120" />
      <el-table-column prop="client_id" label="客户 ID" width="110" />
      <el-table-column prop="username" label="用户名" min-width="160" />
      <el-table-column label="余额（元）" width="130" align="right">
        <template #default="{ row }">
          <span style="font-variant-numeric: tabular-nums">{{ quotaToYuan(row.quota) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="已用（元）" width="130" align="right">
        <template #default="{ row }">
          <span style="color: #909399">{{ quotaToYuan(row.used_quota) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="openAdjust(row)">调额</el-button>
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

    <el-dialog v-model="adjustVisible" title="手工调额" width="480px">
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        title="会写入账本，可追溯"
        description="这笔调整以 admin_adjust 记入 v10_refs，备注里会带上你的账号。"
        style="margin-bottom: 14px"
      />
      <el-form label-width="100px">
        <el-form-item label="目标客户">
          <span>{{ current.username }}（client_id {{ current.client_id }}）</span>
        </el-form-item>
        <el-form-item label="金额（元）">
          <el-input-number v-model="amount" :step="10" :precision="2" style="width: 100%" />
          <div class="cz-hint">正数加额、负数扣额。单笔上限约 4294.97 元。</div>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="remark" placeholder="如：工单 #123 补偿" />
        </el-form-item>
        <el-form-item label="允许负余额">
          <el-switch v-model="allowNeg" />
          <div class="cz-hint">扣额时若余额不足，开启后会扣成负数（欠费）。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="adjustVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitAdjust">确认</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from '../api'
import { fmtTime, quotaToYuan as quotaToYuanRaw } from '../format'

const items = ref([])
const loading = ref(false)
const saving = ref(false)

const keyword = ref('')
const page = ref(1)
const pageSize = 20
const total = ref(0)

const adjustVisible = ref(false)
const current = reactive({ user_id: 0, client_id: 0, username: '' })
const amount = ref(0)
const remark = ref('')
const allowNeg = ref(false)

// QuotaPerUnit 由后端下发；这里先用默认值，页面加载后以后端值为准。
const perUnit = ref(500000)
const quotaToYuan = (q) => quotaToYuanRaw(q, perUnit.value)

async function load() {
  loading.value = true
  try {
    const data = await http.get('/accounts', {
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

function openAdjust(row) {
  current.user_id = row.user_id
  current.client_id = row.client_id
  current.username = row.username
  amount.value = 0
  remark.value = ''
  allowNeg.value = false
  adjustVisible.value = true
}

async function submitAdjust() {
  if (!amount.value) {
    ElMessage.warning('请输入金额')
    return
  }
  const action = amount.value > 0 ? '加额' : '扣额'
  try {
    await ElMessageBox.confirm(
      `确认对 ${current.username} ${action} ${Math.abs(amount.value)} 元？`,
      '确认调额',
      { type: 'warning' }
    )
  } catch {
    return
  }

  saving.value = true
  try {
    await http.post(`/accounts/${current.user_id}/credit`, {
      amount_yuan: amount.value,
      remark: remark.value,
      allow_negative: allowNeg.value,
    })
    ElMessage.success('已入账')
    adjustVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

onMounted(async () => {
  try {
    const ov = await http.get('/overview')
    perUnit.value = Number(ov.quota_per_unit) || 500000
  } catch {
    // 拿不到就用默认值，不影响主流程
  }
  load()
})
</script>
