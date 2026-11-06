<template>
  <div class="cz-page" v-loading="loading">
    <el-row :gutter="14">
      <el-col :span="6" v-for="card in cards" :key="card.label">
        <div class="cz-stat">
          <div class="cz-stat-label">{{ card.label }}</div>
          <div class="cz-stat-value">{{ card.value }}</div>
        </div>
      </el-col>
    </el-row>

    <el-row :gutter="14" style="margin-top: 14px">
      <el-col :span="6" v-for="card in cards2" :key="card.label">
        <div class="cz-stat">
          <div class="cz-stat-label">{{ card.label }}</div>
          <div class="cz-stat-value">{{ card.value }}</div>
        </div>
      </el-col>
    </el-row>

    <el-alert
      style="margin-top: 18px"
      type="info"
      :closable="false"
      title="钱不过网关"
      description="客户在业务系统购买额度与订阅，网关只接收加额指令。此处的手工调额会以 admin_adjust 写入账本，可追溯。"
    />
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import http from '../api'

const loading = ref(false)
const data = ref({})

// quota → 元。QuotaPerUnit 由后端下发，前端不写死，避免两边比例不一致。
const perUnit = computed(() => Number(data.value.quota_per_unit) || 500000)

const cards = computed(() => [
  { label: 'V10 站点', value: data.value.site_count ?? '—' },
  { label: '托管账户', value: data.value.account_count ?? '—' },
  { label: 'Host 总数', value: data.value.host_count ?? '—' },
  { label: '活跃 Host', value: data.value.active_host_count ?? '—' },
])

const cards2 = computed(() => {
  const delta = Number(data.value.today_delta_quota) || 0
  return [
    { label: '今日入账净额（元）', value: (delta / perUnit.value).toFixed(2) },
    { label: '账本凭证数', value: data.value.ref_count ?? '—' },
  ]
})

onMounted(async () => {
  loading.value = true
  try {
    data.value = await http.get('/overview')
  } finally {
    loading.value = false
  }
})
</script>
