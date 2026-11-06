<template>
  <div class="cz-page">
    <div class="cz-toolbar">
      <el-button type="primary" @click="openCreate">
        <el-icon><Plus /></el-icon>&nbsp;新建站点
      </el-button>
      <el-button @click="load">刷新</el-button>
    </div>

    <el-table :data="items" v-loading="loading" border stripe>
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column prop="app_id" label="App ID" min-width="150" />
      <el-table-column prop="name" label="名称" min-width="140" />
      <el-table-column label="密钥前缀" min-width="130">
        <template #default="{ row }">
          <span class="cz-mono">{{ row.secret_prefix }}…</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'info'" size="small">
            {{ row.status === 1 ? '启用' : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="230" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="rotate(row)">轮换密钥</el-button>
          <el-button
            size="small"
            :type="row.status === 1 ? 'warning' : 'success'"
            @click="toggle(row)"
          >
            {{ row.status === 1 ? '停用' : '启用' }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 新建 -->
    <el-dialog v-model="createVisible" title="新建站点" width="460px">
      <el-form :model="form" label-width="90px">
        <el-form-item label="App ID">
          <el-input v-model="form.app_id" placeholder="如 czapi" />
        </el-form-item>
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="留空则与 App ID 相同" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- 密钥展示：只显示一次 -->
    <el-dialog v-model="secretVisible" title="请保存密钥" width="560px">
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        title="此密钥只显示一次"
        :description="secretWarning"
        style="margin-bottom: 12px"
      />
      <div class="cz-secret">{{ secretValue }}</div>
      <template #footer>
        <el-button @click="copySecret">复制</el-button>
        <el-button type="primary" @click="secretVisible = false">我已保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import http from '../api'
import { fmtTime } from '../format'

const items = ref([])
const loading = ref(false)
const saving = ref(false)

const createVisible = ref(false)
const form = reactive({ app_id: '', name: '' })

const secretVisible = ref(false)
const secretValue = ref('')
const secretWarning = ref('')

async function load() {
  loading.value = true
  try {
    const data = await http.get('/sites')
    items.value = data.items || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  form.app_id = ''
  form.name = ''
  createVisible.value = true
}

async function submitCreate() {
  if (!form.app_id) {
    ElMessage.warning('请填写 App ID')
    return
  }
  saving.value = true
  try {
    const data = await http.post('/sites', form)
    createVisible.value = false
    secretValue.value = data.secret
    secretWarning.value = data.warning || ''
    secretVisible.value = true
    await load()
  } finally {
    saving.value = false
  }
}

async function rotate(row) {
  try {
    await ElMessageBox.confirm(
      `轮换后旧密钥立即失效，V10 插件必须同步更新，否则全部请求 401。确定继续？`,
      '轮换密钥',
      { type: 'warning' }
    )
  } catch {
    return
  }
  const data = await http.post(`/sites/${row.id}/secret`)
  secretValue.value = data.secret
  secretWarning.value = data.warning || ''
  secretVisible.value = true
}

async function toggle(row) {
  const enable = row.status !== 1
  try {
    await ElMessageBox.confirm(
      enable ? '启用后该站点恢复接单。' : '停用后该站点所有签名请求返回 403，用于紧急止血。',
      enable ? '启用站点' : '停用站点',
      { type: 'warning' }
    )
  } catch {
    return
  }
  await http.post(`/sites/${row.id}/status`, { enabled: enable })
  ElMessage.success('已更新')
  await load()
}

async function copySecret() {
  try {
    await navigator.clipboard.writeText(secretValue.value)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选中复制')
  }
}

onMounted(load)
</script>
