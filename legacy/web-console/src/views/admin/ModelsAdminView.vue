<script setup lang="ts">
/**
 * 模型与定价 — CRUD plus inline-editable ratios.
 *
 * Ratios are the one field an operator changes daily (upstream price moves), so
 * they are editable in the row itself; everything else goes through the dialog.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { AdminModel } from '@/types/api'
import { fmtInt, fmtMoney } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const models = ref<AdminModel[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const keyword = ref('')
const group = ref('')

async function load() {
  loading.value = true
  error.value = null
  try {
    models.value = await adminApi.listModels()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载模型失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const groups = computed(() => [...new Set(models.value.map((m) => m.group_name))])

const filtered = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return models.value.filter((m) => {
    if (group.value && m.group_name !== group.value) return false
    if (!q) return true
    return m.model_name.toLowerCase().includes(q) || m.display_name.toLowerCase().includes(q)
  })
})

/* ------------------------------------------------------------------ */
/* Dialog                                                             */
/* ------------------------------------------------------------------ */

const dialogOpen = ref(false)
const mode = ref<'create' | 'edit'>('create')
const saving = ref(false)
const originalName = ref<string | null>(null)

const blank = (): AdminModel => ({
  model_name: '',
  display_name: '',
  group_name: 'default',
  context_len: 65536,
  model_ratio: 1,
  completion_ratio: 1,
  cached_ratio: 0.5,
  input_micros: 0,
  output_micros: 0,
  capabilities: [],
  status: 1,
})

const form = reactive<AdminModel>(blank())
const formRef = ref<{ validate: () => Promise<boolean> } | null>(null)

const rules = {
  model_name: [
    { required: true, message: '模型名必填（客户端请求时使用的名字）', trigger: 'blur' },
    { pattern: /^[a-zA-Z0-9._:-]+$/, message: '只允许字母、数字与 . _ : -', trigger: 'blur' },
  ],
  display_name: [{ required: true, message: '显示名必填', trigger: 'blur' }],
}

function openCreate() {
  Object.assign(form, blank())
  originalName.value = null
  mode.value = 'create'
  dialogOpen.value = true
}

function openEdit(row: AdminModel) {
  Object.assign(form, JSON.parse(JSON.stringify(row)) as AdminModel)
  originalName.value = row.model_name
  mode.value = 'edit'
  dialogOpen.value = true
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  saving.value = true
  try {
    if (mode.value === 'edit' && originalName.value) {
      await adminApi.updateModel(originalName.value, { ...form })
      ElMessage.success('模型已更新')
    } else {
      await adminApi.upsertModel({ ...form })
      ElMessage.success('模型已保存（按 model_name upsert）')
    }
    dialogOpen.value = false
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    saving.value = false
  }
}

/* ------------------------------------------------------------------ */
/* Inline ratio editing                                               */
/* ------------------------------------------------------------------ */

const editingRatio = ref<string | null>(null)
const ratioDraft = reactive({ model_ratio: 1, completion_ratio: 1, cached_ratio: 0.5 })
const ratioSaving = ref(false)

function startRatio(row: AdminModel) {
  editingRatio.value = row.model_name
  ratioDraft.model_ratio = row.model_ratio
  ratioDraft.completion_ratio = row.completion_ratio
  ratioDraft.cached_ratio = row.cached_ratio
}

async function saveRatio(row: AdminModel) {
  ratioSaving.value = true
  try {
    const updated = await adminApi.updateModel(row.model_name, {
      model_ratio: ratioDraft.model_ratio,
      completion_ratio: ratioDraft.completion_ratio,
      cached_ratio: ratioDraft.cached_ratio,
    })
    const i = models.value.findIndex((m) => m.model_name === row.model_name)
    if (i >= 0) models.value[i] = updated
    editingRatio.value = null
    ElMessage.success('倍率已保存')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    ratioSaving.value = false
  }
}

async function toggleStatus(row: AdminModel) {
  try {
    const updated = await adminApi.updateModel(row.model_name, { status: row.status === 1 ? 0 : 1 })
    const i = models.value.findIndex((m) => m.model_name === row.model_name)
    if (i >= 0) models.value[i] = updated
    ElMessage.success(row.status === 1 ? '模型已下线' : '模型已上线')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  }
}

async function remove(row: AdminModel) {
  try {
    await ElMessageBox.confirm(
      `删除模型「${row.model_name}」后，所有引用它的渠道映射与 Key 模型范围都会失效。确认删除？`,
      '确认删除',
      { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }
  try {
    await adminApi.deleteModel(row.model_name)
    ElMessage.success('模型已删除')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}

const CAPS = [
  { value: 'vision', label: '视觉' },
  { value: 'tools', label: '工具调用' },
  { value: 'json', label: 'JSON' },
  { value: 'reasoning', label: '推理' },
]

const capLabel = (c: string) => CAPS.find((x) => x.value === c)?.label ?? c
</script>

<template>
  <div class="page">
    <PageHeader title="模型与定价" desc="倍率决定计费：cost_tokens = (prompt + completion×补全倍率 − cached×(1−缓存倍率)) × 模型倍率 × 分组倍率。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
        <el-button type="primary" @click="openCreate">
          <Icon name="plus" :size="15" /><span class="btn-gap">新增模型</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-input v-model="keyword" placeholder="搜索模型" clearable style="width: 240px">
        <template #prefix><Icon name="search" :size="14" /></template>
      </el-input>
      <el-radio-group v-model="group" size="default">
        <el-radio-button value="">全部分组</el-radio-button>
        <el-radio-button v-for="g in groups" :key="g" :value="g">{{ g }}</el-radio-button>
      </el-radio-group>
      <span class="toolbar__spacer" />
      <span class="toolbar__note">倍率可直接在行内编辑</span>
    </div>

    <section v-if="loading" class="panel">
      <div class="panel__body">
        <div v-for="i in 5" :key="i" class="row-skeleton">
          <span class="sk" style="width: 18%; height: 12px" />
          <span class="sk" style="width: 25%; height: 12px" />
          <span class="sk" style="width: 14%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error" class="panel">
      <EmptyState icon="alert" title="模型列表加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!filtered.length" class="panel">
      <EmptyState icon="layers" title="没有匹配的模型" desc="新增模型或调整筛选条件。">
        <el-button type="primary" size="small" @click="openCreate">新增模型</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="filtered" row-key="model_name">
        <el-table-column label="模型" min-width="215">
          <template #default="{ row }">
            <div class="cell-model">
              <span class="cell-model__name">{{ row.display_name }}</span>
              <code class="cell-model__id">{{ row.model_name }}</code>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="分组" width="110">
          <template #default="{ row }"><code class="mono group-tag">{{ row.group_name }}</code></template>
        </el-table-column>

        <el-table-column label="上下文" width="104" align="right" class-name="col-num">
          <template #default="{ row }"><span class="mono">{{ fmtInt(row.context_len / 1024) }}K</span></template>
        </el-table-column>

        <el-table-column label="倍率（行内编辑）" min-width="250">
          <template #default="{ row }">
            <div v-if="editingRatio === row.model_name" class="ratio-edit">
              <el-input-number v-model="ratioDraft.model_ratio" :min="0" :step="0.05" :precision="3" size="small"
                controls-position="right" style="width: 96px" />
              <el-input-number v-model="ratioDraft.completion_ratio" :min="0" :step="0.1" :precision="2" size="small"
                controls-position="right" style="width: 92px" />
              <el-input-number v-model="ratioDraft.cached_ratio" :min="0" :max="1" :step="0.05" :precision="2" size="small"
                controls-position="right" style="width: 92px" />
              <el-button text size="small" type="primary" :loading="ratioSaving" @click="saveRatio(row)">保存</el-button>
              <el-button text size="small" @click="editingRatio = null">取消</el-button>
            </div>
            <div v-else class="ratio-view" @click="startRatio(row)">
              <span class="mono ratio-view__main">×{{ row.model_ratio }}</span>
              <span class="mono ratio-view__sub">补全 ×{{ row.completion_ratio }} · 缓存 ×{{ row.cached_ratio }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="绝对价（输入/输出 每 1M）" width="184" align="right" class-name="col-num">
          <template #default="{ row }">
            <span v-if="!row.input_micros && !row.output_micros" class="dim">按倍率</span>
            <span v-else class="mono">{{ fmtMoney(row.input_micros) }} / {{ fmtMoney(row.output_micros) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="能力" min-width="160">
          <template #default="{ row }">
            <div class="chips">
              <span v-for="c in row.capabilities" :key="c" class="chip">{{ capLabel(c) }}</span>
              <span v-if="!row.capabilities.length" class="dim">—</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="102">
          <template #default="{ row }">
            <span class="badge" :class="row.status === 1 ? 'badge--ok' : 'badge--idle'">{{ row.status === 1 ? '上线' : '下线' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="184" fixed="right" align="right">
          <template #default="{ row }">
            <div class="row-actions">
              <el-button text size="small" @click="toggleStatus(row)">{{ row.status === 1 ? '下线' : '上线' }}</el-button>
              <el-button text size="small" @click="openEdit(row)">编辑</el-button>
              <el-button text size="small" type="danger" @click="remove(row)">删除</el-button>
            </div>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- ============ dialog ============ -->
    <el-dialog v-model="dialogOpen" :title="mode === 'create' ? '新增模型' : `编辑模型 ${originalName}`" width="600px"
      append-to-body>
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent>
        <div class="form-grid">
          <el-form-item label="模型名（model_name）" prop="model_name" style="grid-column: 1 / -1">
            <el-input v-model="form.model_name" :disabled="mode === 'edit'" class="mono-input" placeholder="deepseek-chat" />
            <div class="hint">客户端在 <code class="mono">model</code> 字段中使用的名字；保存时按其 upsert。</div>
          </el-form-item>

          <el-form-item label="显示名" prop="display_name">
            <el-input v-model="form.display_name" placeholder="DeepSeek Chat" />
          </el-form-item>

          <el-form-item label="分组">
            <el-input v-model="form.group_name" placeholder="default / premium" />
          </el-form-item>

          <el-form-item label="上下文长度（token）">
            <el-input-number v-model="form.context_len" :min="1024" :step="1024" controls-position="right" style="width: 100%" />
          </el-form-item>

          <el-form-item label="状态">
            <el-switch v-model="form.status" :active-value="1" :inactive-value="0" inline-prompt active-text="上线" inactive-text="下线" />
          </el-form-item>

          <el-form-item label="模型倍率">
            <el-input-number v-model="form.model_ratio" :min="0" :step="0.05" :precision="3" controls-position="right" style="width: 100%" />
          </el-form-item>

          <el-form-item label="补全倍率">
            <el-input-number v-model="form.completion_ratio" :min="0" :step="0.1" :precision="2" controls-position="right" style="width: 100%" />
          </el-form-item>

          <el-form-item label="缓存折扣倍率">
            <el-input-number v-model="form.cached_ratio" :min="0" :max="1" :step="0.05" :precision="2" controls-position="right" style="width: 100%" />
          </el-form-item>
        </div>

        <el-form-item label="绝对价（微元 / 1M token，填 0 表示按倍率计费）">
          <div class="form-grid" style="width: 100%">
            <el-input-number v-model="form.input_micros" :min="0" :step="50000" controls-position="right" style="width: 100%" />
            <el-input-number v-model="form.output_micros" :min="0" :step="50000" controls-position="right" style="width: 100%" />
          </div>
          <div class="hint">输入 / 输出。非 0 时改用绝对价公式，倍率不再参与。</div>
        </el-form-item>

        <el-form-item label="能力标签">
          <el-select v-model="form.capabilities" multiple allow-create filterable default-first-option style="width: 100%"
            placeholder="选择或输入">
            <el-option v-for="c in CAPS" :key="c.value" :label="c.label" :value="c.value" />
          </el-select>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="dialogOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </template>
    </el-dialog>
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

.cell-model {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.cell-model__name {
  color: var(--ink-1);
  font-weight: 550;
}

.cell-model__id {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.group-tag {
  font-size: var(--fs-2xs);
  padding: 1px 6px;
  border-radius: var(--r-xs);
  background: var(--bg-inset);
  border: 1px solid var(--line-1);
  color: var(--ink-3);
}

.ratio-view {
  display: flex;
  flex-direction: column;
  gap: 0;
  cursor: pointer;
  border-radius: var(--r-xs);
  padding: 2px 4px;
  margin-left: -4px;
}

.ratio-view:hover {
  background: var(--bg-hover);
}

.ratio-view__main {
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--ink-1);
}

.ratio-view__sub {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.ratio-edit {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}

.chips {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.chip {
  padding: 1px 6px;
  border-radius: var(--r-xs);
  border: 1px solid color-mix(in srgb, var(--steel) 26%, transparent);
  background: var(--steel-soft);
  color: var(--steel);
  font-size: var(--fs-2xs);
  white-space: nowrap;
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

:deep(.mono-input .el-input__inner) {
  font-family: var(--font-mono);
}
</style>
