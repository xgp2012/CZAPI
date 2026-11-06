<script setup lang="ts">
/**
 * 渠道管理 — CRUD + key pool + connectivity test + model sync.
 *
 * Key pool semantics are the interesting part: the gateway only ever returns a
 * masked tail, so the editor shows existing tails as read-only text and offers a
 * "replace secret" input that is write-only. Blank = keep the current secret.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { Channel, ChannelKeySlotWrite, ChannelWrite } from '@/types/api'
import { fmtInt, fmtMs, fmtPercent } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const channels = ref<Channel[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

const keyword = ref('')
const filterStatus = ref<'' | '1' | '0'>('')

async function load() {
  loading.value = true
  error.value = null
  try {
    channels.value = await adminApi.listChannels()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载渠道失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const filtered = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return channels.value.filter((c) => {
    if (filterStatus.value && String(c.status) !== filterStatus.value) return false
    if (!q) return true
    return c.name.toLowerCase().includes(q) || c.base_url.toLowerCase().includes(q) || c.models.some((m) => m.toLowerCase().includes(q))
  })
})

/* ------------------------------------------------------------------ */
/* Drawer: create / edit                                              */
/* ------------------------------------------------------------------ */

interface KeySlotForm {
  /** Blank keeps the existing secret (edit mode) or skips the slot (create). */
  secret: string
  weight: number
  status: string
  /** Read-only masked tail carried over from the server. */
  masked: string | null
}

const drawerOpen = ref(false)
const drawerMode = ref<'create' | 'edit'>('create')
const saving = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<{ validate: () => Promise<boolean> } | null>(null)

const form = reactive({
  name: '',
  provider: 'openai',
  base_url: '',
  models: [] as string[],
  model_map_text: '{\n  \n}',
  weight: 100,
  priority: 0,
  max_concurrency: 100,
  timeout_ms: 300_000,
  status: 1,
  key_pool: [] as KeySlotForm[],
})

const rules = {
  name: [{ required: true, message: '渠道名称必填', trigger: 'blur' }],
  base_url: [
    { required: true, message: '上游地址必填', trigger: 'blur' },
    { pattern: /^https?:\/\/.+/, message: '需要以 http:// 或 https:// 开头', trigger: 'blur' },
  ],
  models: [{ required: true, type: 'array' as const, min: 1, message: '至少配置一个模型', trigger: 'change' }],
}

function resetForm() {
  form.name = ''
  form.provider = 'openai'
  form.base_url = ''
  form.models = []
  form.model_map_text = '{\n  \n}'
  form.weight = 100
  form.priority = 0
  form.max_concurrency = 100
  form.timeout_ms = 300_000
  form.status = 1
  form.key_pool = [{ secret: '', weight: 1, status: 'active', masked: null }]
}

function openCreate() {
  drawerMode.value = 'create'
  editingId.value = null
  resetForm()
  drawerOpen.value = true
}

function openEdit(row: Channel) {
  drawerMode.value = 'edit'
  editingId.value = row.channel_id
  form.name = row.name
  form.provider = row.provider
  form.base_url = row.base_url
  form.models = [...row.models]
  form.model_map_text = JSON.stringify(row.model_map ?? {}, null, 2)
  form.weight = row.weight
  form.priority = row.priority
  form.max_concurrency = row.max_concurrency
  form.timeout_ms = row.timeout_ms
  form.status = row.status
  form.key_pool = row.key_pool.length
    ? row.key_pool.map((k) => ({ secret: '', weight: k.weight, status: k.status, masked: k.masked }))
    : [{ secret: '', weight: 1, status: 'active', masked: null }]
  drawerOpen.value = true
}

const modelMapError = ref<string | null>(null)

function parseModelMap(): Record<string, string> | null {
  const raw = form.model_map_text.trim()
  if (!raw || raw === '{}') return {}
  try {
    const parsed = JSON.parse(raw) as unknown
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      modelMapError.value = 'model_map 必须是 JSON 对象，例如 {"gpt-4o-mini":"deepseek-chat"}'
      return null
    }
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v !== 'string') {
        modelMapError.value = `键 "${k}" 的值必须是字符串（目标模型名）`
        return null
      }
    }
    modelMapError.value = null
    return parsed as Record<string, string>
  } catch (e) {
    modelMapError.value = `JSON 解析失败：${e instanceof Error ? e.message : '格式错误'}`
    return null
  }
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const map = parseModelMap()
  if (map === null) return

  if (drawerMode.value === 'create' && !form.key_pool.some((k) => k.secret.trim())) {
    ElMessage.warning('至少填写一个上游密钥（写入后只显示尾 4 位）')
    return
  }

  const key_pool: ChannelKeySlotWrite[] = form.key_pool
    .filter((k) => k.secret.trim() || drawerMode.value === 'edit')
    .map((k) => ({ secret: k.secret.trim() || undefined, weight: k.weight, status: k.status }))

  const payload: ChannelWrite = {
    name: form.name.trim(),
    provider: form.provider,
    base_url: form.base_url.trim(),
    models: [...form.models],
    model_map: map,
    weight: form.weight,
    priority: form.priority,
    max_concurrency: form.max_concurrency,
    timeout_ms: form.timeout_ms,
    status: form.status,
    key_pool,
  }

  saving.value = true
  try {
    if (drawerMode.value === 'create') {
      await adminApi.createChannel(payload)
      ElMessage.success('渠道已创建')
    } else if (editingId.value !== null) {
      await adminApi.updateChannel(editingId.value, payload)
      ElMessage.success('渠道已更新')
    }
    drawerOpen.value = false
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    saving.value = false
  }
}

function addKeySlot() {
  form.key_pool.push({ secret: '', weight: 1, status: 'active', masked: null })
}

function removeKeySlot(i: number) {
  form.key_pool.splice(i, 1)
}

/* ------------------------------------------------------------------ */
/* Row actions                                                        */
/* ------------------------------------------------------------------ */

const busyId = ref<number | null>(null)
const testResult = ref<Record<number, { ok: boolean; message: string; latency_ms: number }>>({})

async function testChannel(row: Channel) {
  busyId.value = row.channel_id
  try {
    const res = await adminApi.testChannel(row.channel_id)
    testResult.value = { ...testResult.value, [row.channel_id]: res }
    if (res.ok) ElMessage.success(`连通正常 · ${fmtMs(res.latency_ms)}`)
    else ElMessage.warning(res.message)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '探测失败')
  } finally {
    busyId.value = null
  }
}

async function syncModels(row: Channel) {
  busyId.value = row.channel_id
  try {
    const res = await adminApi.syncChannelModels(row.channel_id)
    ElMessage.success(`已同步，上游返回 ${res.total} 个模型` + (res.added.length ? `，新增 ${res.added.length} 个` : '，无新增'))
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '同步失败')
  } finally {
    busyId.value = null
  }
}

async function toggleStatus(row: Channel) {
  try {
    await adminApi.updateChannel(row.channel_id, { status: row.status === 1 ? 0 : 1 })
    ElMessage.success(row.status === 1 ? '渠道已停用' : '渠道已启用')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  }
}

async function remove(row: Channel) {
  try {
    await ElMessageBox.confirm(
      `删除渠道「${row.name}」后，绑定到它的模型将失去上游。如果只是临时下线，建议改用「停用」。`,
      '确认删除渠道',
      { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }
  try {
    await adminApi.deleteChannel(row.channel_id)
    ElMessage.success('渠道已删除')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

const healthTone = (c: Channel) => (c.health.open ? 'danger' : c.status === 0 ? 'idle' : 'ok')
</script>

<template>
  <div class="page">
    <PageHeader title="渠道管理" desc="上游渠道、密钥池、模型映射与熔断状态。密钥写入后只回显尾 4 位，网关以 AES-GCM 加密存储。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
        <el-button type="primary" @click="openCreate">
          <Icon name="plus" :size="15" /><span class="btn-gap">新建渠道</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-input v-model="keyword" placeholder="搜索渠道名 / 地址 / 模型" clearable style="width: 280px">
        <template #prefix><Icon name="search" :size="14" /></template>
      </el-input>
      <el-radio-group v-model="filterStatus" size="default">
        <el-radio-button value="">全部</el-radio-button>
        <el-radio-button value="1">启用</el-radio-button>
        <el-radio-button value="0">停用</el-radio-button>
      </el-radio-group>
      <span class="toolbar__spacer" />
      <span class="toolbar__note">共 <b class="mono">{{ filtered.length }}</b> 个渠道</span>
    </div>

    <section v-if="loading" class="panel">
      <div class="panel__body">
        <div v-for="i in 4" :key="i" class="row-skeleton">
          <span class="sk" style="width: 18%; height: 12px" />
          <span class="sk" style="width: 24%; height: 12px" />
          <span class="sk" style="width: 12%; height: 12px" />
          <span class="sk" style="width: 10%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error" class="panel">
      <EmptyState icon="alert" title="渠道列表加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!filtered.length" class="panel">
      <EmptyState icon="server" title="没有匹配的渠道" :desc="channels.length ? '换个关键词或清空筛选。' : '添加第一个上游渠道后，模型才能真正被调用。'">
        <el-button v-if="!channels.length" type="primary" size="small" @click="openCreate">新建渠道</el-button>
        <el-button v-else size="small" @click="keyword = ''; filterStatus = ''">重置筛选</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="filtered" row-key="channel_id">
        <el-table-column label="渠道" min-width="210">
          <template #default="{ row }">
            <div class="cell-chan">
              <span class="cell-chan__name">{{ row.name }}</span>
              <span class="cell-chan__meta mono">#{{ row.channel_id }} · {{ row.provider }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="上游地址" min-width="230">
          <template #default="{ row }">
            <code class="cell-url" :title="row.base_url">{{ row.base_url }}</code>
          </template>
        </el-table-column>

        <el-table-column label="模型" min-width="200">
          <template #default="{ row }">
            <div class="chips">
              <span v-for="m in row.models.slice(0, 3)" :key="m" class="chip">{{ m }}</span>
              <span v-if="row.models.length > 3" class="chip chip--more">+{{ row.models.length - 3 }}</span>
            </div>
            <span v-if="Object.keys(row.model_map).length" class="mapnote mono">映射 {{ Object.keys(row.model_map).length }} 条</span>
          </template>
        </el-table-column>

        <el-table-column label="密钥池" width="132">
          <template #default="{ row }">
            <div class="pool">
              <span v-for="k in row.key_pool" :key="k.index" class="pool__slot mono" :class="{ 'is-off': k.status !== 'active' }">
                {{ k.masked }}
              </span>
              <span v-if="!row.key_pool.length" class="dim">空</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="权重 / 优先级" width="132" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono">{{ row.weight }}</span>
            <span class="dim"> / </span>
            <span class="mono">{{ row.priority }}</span>
          </template>
        </el-table-column>

        <el-table-column label="并发 / 超时" width="132" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono">{{ fmtInt(row.max_concurrency) }}</span>
            <span class="dim"> / </span>
            <span class="mono">{{ row.timeout_ms / 1000 }}s</span>
          </template>
        </el-table-column>

        <el-table-column label="错误率" width="104" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono" :class="{ 'is-hot': row.health.error_rate > 0.05 }">{{ fmtPercent(row.health.error_rate) }}</span>
            <span class="samples mono">{{ row.health.samples }} 样本</span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <span class="badge" :class="`badge--${healthTone(row)}`">
              {{ row.health.open ? '熔断' : row.status === 1 ? '启用' : '停用' }}
            </span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="252" fixed="right" align="right">
          <template #default="{ row }">
            <div class="row-actions">
              <el-button text size="small" :loading="busyId === row.channel_id" @click="testChannel(row)">测试</el-button>
              <el-button text size="small" :loading="busyId === row.channel_id" @click="syncModels(row)">同步模型</el-button>
              <el-button text size="small" @click="openEdit(row)">编辑</el-button>
              <el-dropdown trigger="click" @command="(c: string) => (c === 'toggle' ? toggleStatus(row) : remove(row))">
                <el-button text size="small">更多<Icon name="chevronDown" :size="12" /></el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="toggle">{{ row.status === 1 ? '停用渠道' : '启用渠道' }}</el-dropdown-item>
                    <el-dropdown-item command="delete" style="color: var(--danger)">删除渠道</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </div>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- ============ create / edit drawer ============ -->
    <el-drawer v-model="drawerOpen" :title="drawerMode === 'create' ? '新建渠道' : `编辑渠道 #${editingId}`" size="560px"
      :destroy-on-close="false">
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent>
        <div class="form-row">
          <el-form-item label="名称" prop="name" style="flex: 1">
            <el-input v-model="form.name" placeholder="deepseek 官方" />
          </el-form-item>
          <el-form-item label="驱动" style="width: 148px">
            <el-select v-model="form.provider">
              <el-option label="openai" value="openai" />
              <el-option label="azure" value="azure" />
              <el-option label="anthropic" value="anthropic" />
            </el-select>
          </el-form-item>
        </div>

        <el-form-item label="上游 Base URL" prop="base_url">
          <el-input v-model="form.base_url" placeholder="https://api.deepseek.com" />
          <div class="hint">网关会拼接 <code class="mono">/v1/chat/completions</code>，请填到根路径。</div>
        </el-form-item>

        <el-form-item label="模型列表" prop="models">
          <el-select v-model="form.models" multiple filterable allow-create default-first-option :reserve-keyword="false"
            placeholder="输入模型名后回车" style="width: 100%" />
          <div class="hint">上游支持的模型名。「同步模型」可自动拉取 <code class="mono">GET /v1/models</code> 合并。</div>
        </el-form-item>

        <el-form-item label="模型映射 model_map">
          <el-input v-model="form.model_map_text" type="textarea" :rows="5" class="mono-input"
            placeholder='{ "gpt-4o-mini": "deepseek-chat" }' />
          <div class="hint">
            客户端请求左边 → 上游实际调用右边。必须是 JSON 对象。
            <span v-if="modelMapError" class="err">{{ modelMapError }}</span>
          </div>
        </el-form-item>

        <el-form-item label="密钥池（写-only）">
          <div class="pool-editor">
            <div v-for="(k, i) in form.key_pool" :key="i" class="pool-row">
              <div class="pool-row__head">
                <span class="pool-row__idx mono">#{{ i }}</span>
                <span v-if="k.masked" class="pool-row__masked mono" title="已存储的密钥尾号">{{ k.masked }}</span>
                <span v-else class="pool-row__masked dim">未设置</span>
                <el-button text size="small" type="danger" :disabled="form.key_pool.length === 1" @click="removeKeySlot(i)">
                  移除
                </el-button>
              </div>
              <div class="pool-row__fields">
                <el-input v-model="k.secret" type="password" show-password :placeholder="k.masked ? '留空 = 保持原密钥' : 'sk-...'" />
                <el-input-number v-model="k.weight" :min="1" :max="100" size="default" controls-position="right" style="width: 96px" />
                <el-select v-model="k.status" style="width: 104px">
                  <el-option label="active" value="active" />
                  <el-option label="disabled" value="disabled" />
                </el-select>
              </div>
            </div>
            <el-button text size="small" @click="addKeySlot">
              <Icon name="plus" :size="13" /><span class="btn-gap">再加一个密钥</span>
            </el-button>
            <div class="hint">写入后只能看到尾 4 位。同一渠道内多把密钥按权重轮询。</div>
          </div>
        </el-form-item>

        <div class="form-grid">
          <el-form-item label="权重">
            <el-input-number v-model="form.weight" :min="0" :max="1000" controls-position="right" style="width: 100%" />
          </el-form-item>
          <el-form-item label="优先级">
            <el-input-number v-model="form.priority" :min="-100" :max="100" controls-position="right" style="width: 100%" />
          </el-form-item>
          <el-form-item label="最大并发">
            <el-input-number v-model="form.max_concurrency" :min="1" :max="10000" controls-position="right" style="width: 100%" />
          </el-form-item>
          <el-form-item label="超时（毫秒）">
            <el-input-number v-model="form.timeout_ms" :min="1000" :max="600000" :step="10000" controls-position="right" style="width: 100%" />
          </el-form-item>
        </div>

        <el-form-item label="状态">
          <el-switch v-model="form.status" :active-value="1" :inactive-value="0" inline-prompt active-text="启用"
            inactive-text="停用" />
          <span class="hint" style="margin-left: 10px">停用后不参与选路，但配置保留。</span>
        </el-form-item>
      </el-form>

      <template #footer>
        <div class="drawer-foot">
          <el-button @click="drawerOpen = false">取消</el-button>
          <el-button type="primary" :loading="saving" @click="submit">
            {{ drawerMode === 'create' ? '创建' : '保存' }}
          </el-button>
        </div>
      </template>
    </el-drawer>
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

.cell-chan {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.cell-chan__name {
  color: var(--ink-1);
  font-weight: 550;
}

.cell-chan__meta {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.cell-url {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chips {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.chip {
  padding: 1px 6px;
  border-radius: var(--r-xs);
  background: var(--bg-inset);
  border: 1px solid var(--line-1);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-2);
  white-space: nowrap;
}

.chip--more {
  color: var(--ink-3);
}

.mapnote {
  display: block;
  margin-top: 3px;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.pool {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.pool__slot {
  font-size: var(--fs-2xs);
  color: var(--ink-2);
}

.pool__slot.is-off {
  color: var(--ink-4);
  text-decoration: line-through;
}

.samples {
  display: block;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.is-hot {
  color: var(--danger) !important;
  font-weight: 600;
}

/* ---- drawer ---- */
.form-row {
  display: flex;
  gap: var(--sp-3);
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 var(--sp-4);
}

.hint {
  margin-top: 4px;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  line-height: 1.55;
}

.hint .err {
  display: block;
  color: var(--danger);
  margin-top: 2px;
}

:deep(.mono-input .el-textarea__inner) {
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  line-height: 1.65;
}

.pool-editor {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: var(--sp-3);
}

.pool-row {
  padding: var(--sp-3);
  border: 1px solid var(--line-1);
  border-radius: var(--r-sm);
  background: var(--bg-sunken);
}

.pool-row__head {
  display: flex;
  align-items: center;
  gap: var(--sp-2);
  margin-bottom: var(--sp-2);
}

.pool-row__idx {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.pool-row__masked {
  font-size: var(--fs-2xs);
  color: var(--ink-2);
  margin-right: auto;
}

.pool-row__fields {
  display: flex;
  gap: var(--sp-2);
}

.drawer-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--sp-2);
}
</style>
