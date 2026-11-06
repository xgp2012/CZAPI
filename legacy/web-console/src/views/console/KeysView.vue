<script setup lang="ts">
/**
 * Key 管理.
 *
 * The one genuinely dangerous screen in the user centre: creating a key shows
 * the plaintext exactly once. That moment gets a dedicated modal with a bold
 * warning, a copy button and a "I have saved it" acknowledgement, because the
 * gateway stores only a hash and cannot show it again.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import dayjs from 'dayjs'
import { useKeysStore } from '@/stores/keys'
import { useOverviewStore } from '@/stores/overview'
import type { ApiKey, CreateKeyRequest } from '@/types/api'
import { copyText, fmtDateTime, fmtMoney, fmtRelative, fmtTokens } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'
import CopyButton from '@/components/CopyButton.vue'

const store = useKeysStore()
const overview = useOverviewStore()

const error = ref<string | null>(null)
const freshKeyVisible = ref(false)

onMounted(async () => {
  try {
    await store.load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
    ElMessage.error(error.value)
  }
  try {
    const o = await overview.load()
    store.setSubscriptions(o.subscriptions)
  } catch {
    /* the create dialog falls back to the first subscription */
  }
})

const subOptions = computed(() =>
  store.subscriptions.map((s) => ({
    value: s.subscription_id,
    label: `${s.plan_name} · #${s.subscription_id}${s.status === 'active' ? '' : '（' + s.status + '）'}`,
  })),
)

const modelOptions = computed(() => store.models.map((m) => ({ value: m.model_name, label: m.display_name })))

/* ------------------------------------------------------------------ */
/* Filters                                                            */
/* ------------------------------------------------------------------ */

const filterName = ref('')
const filterStatus = ref<'' | ApiKey['status']>('')

const filtered = computed(() =>
  store.keys.filter((k) => {
    if (filterStatus.value && k.status !== filterStatus.value) return false
    const q = filterName.value.trim().toLowerCase()
    if (!q) return true
    return k.name.toLowerCase().includes(q) || k.key_prefix.toLowerCase().includes(q)
  }),
)

const counts = computed(() => ({
  all: store.keys.length,
  active: store.keys.filter((k) => k.status === 'active').length,
  suspended: store.keys.filter((k) => k.status === 'suspended').length,
  revoked: store.keys.filter((k) => k.status === 'revoked').length,
}))

/* ------------------------------------------------------------------ */
/* Create dialog                                                      */
/* ------------------------------------------------------------------ */

const createOpen = ref(false)
const creating = ref(false)
const formRef = ref<{ validate: () => Promise<boolean>; clearValidate: () => void } | null>(null)

const form = reactive({
  name: '',
  subscription_id: undefined as number | undefined,
  models: [] as string[],
  ip_whitelist: [] as string[],
  /** Element Plus date-picker value (formatted string); converted to unix秒 on submit. */
  expires_at: null as string | null,
})

const rules = {
  name: [
    { required: true, message: '给这个 Key 起个名字，便于区分环境', trigger: 'blur' },
    { min: 2, max: 40, message: '名称长度 2–40 个字符', trigger: 'blur' },
  ],
  subscription_id: [{ required: true, message: '选择这个 Key 归属的订阅', trigger: 'change' }],
  models: [{ required: true, type: 'array' as const, min: 1, message: '至少选择一个模型', trigger: 'change' }],
}

function openCreate() {
  form.name = ''
  form.subscription_id = store.subscriptions.find((s) => s.status === 'active')?.subscription_id ?? store.subscriptions[0]?.subscription_id
  form.models = modelOptions.value.slice(0, 1).map((m) => m.value)
  form.ip_whitelist = []
  form.expires_at = null
  createOpen.value = true
  requestAnimationFrame(() => formRef.value?.clearValidate())
}

async function submitCreate() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  creating.value = true
  try {
    const payload: CreateKeyRequest = {
      name: form.name.trim(),
      subscription_id: form.subscription_id!,
      models: [...form.models],
      ip_whitelist: [...form.ip_whitelist],
      expires_at: form.expires_at ? dayjs(form.expires_at).unix() : null,
    }
    await store.create(payload)
    createOpen.value = false
    freshKeyVisible.value = true
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '创建失败')
  } finally {
    creating.value = false
  }
}

/* ------------------------------------------------------------------ */
/* Edit dialog                                                        */
/* ------------------------------------------------------------------ */

const editOpen = ref(false)
const editing = ref<ApiKey | null>(null)
const editForm = reactive({
  name: '',
  models: [] as string[],
  ip_whitelist: [] as string[],
  expires_at: null as string | null,
})

function openEdit(row: ApiKey) {
  editing.value = row
  editForm.name = row.name
  editForm.models = [...row.models]
  editForm.ip_whitelist = [...row.ip_whitelist]
  editForm.expires_at = row.expires_at ? dayjs(row.expires_at * 1000).format('YYYY-MM-DD HH:mm:ss') : null
  editOpen.value = true
}

async function submitEdit() {
  if (!editing.value) return
  if (editForm.name.trim().length < 2) {
    ElMessage.warning('名称至少 2 个字符')
    return
  }
  if (!editForm.models.length) {
    ElMessage.warning('至少保留一个模型')
    return
  }
  try {
    await store.update(editing.value.key_id, {
      name: editForm.name.trim(),
      models: [...editForm.models],
      ip_whitelist: [...editForm.ip_whitelist],
      expires_at: editForm.expires_at ? dayjs(editForm.expires_at).unix() : null,
    })
    ElMessage.success('已保存修改')
    editOpen.value = false
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  }
}

/* ------------------------------------------------------------------ */
/* Row actions                                                        */
/* ------------------------------------------------------------------ */

const toggling = ref<number | null>(null)

async function onToggle(row: ApiKey, next: boolean) {
  toggling.value = row.key_id
  try {
    await store.toggle(row.key_id, next)
    ElMessage.success(next ? `已启用「${row.name}」` : `已停用「${row.name}」`)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  } finally {
    toggling.value = null
  }
}

async function onRevoke(row: ApiKey) {
  try {
    await ElMessageBox.confirm(
      `吊销「${row.name}」后该 Key 立即失效，正在使用它的服务会收到 401。此操作不可撤销。`,
      '确认吊销',
      { confirmButtonText: '吊销', cancelButtonText: '取消', type: 'warning', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }
  try {
    await store.revoke(row.key_id)
    ElMessage.success('Key 已吊销')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '吊销失败')
  }
}

function closeFresh() {
  freshKeyVisible.value = false
  store.clearFreshKey()
}

async function copyFresh() {
  const key = store.freshKey?.key
  if (!key) return
  const ok = await copyText(key)
  if (ok) ElMessage.success('已复制明文 Key')
  else ElMessage.error('复制失败，请手动选中复制')
}

/* ------------------------------------------------------------------ */
/* Display helpers                                                    */
/* ------------------------------------------------------------------ */

function statusMeta(s: ApiKey['status']) {
  return {
    active: { label: '生效中', tone: 'ok' },
    suspended: { label: '已停用', tone: 'warn' },
    revoked: { label: '已吊销', tone: 'danger' },
  }[s]
}

const maskKey = (prefix: string) => `${prefix}${'•'.repeat(18)}`

const MODEL_LABELS = computed(() => new Map(store.models.map((m) => [m.model_name, m.display_name])))
const modelLabel = (name: string) => MODEL_LABELS.value.get(name) ?? name

/** Revoked rows fade out but stay readable. */
const rowClassName = ({ row }: { row: ApiKey }) => (row.status === 'revoked' ? 'is-revoked' : '')
</script>

<template>
  <div class="page">
    <PageHeader title="Key 管理" desc="每把 Key 绑定一个订阅，可限制模型范围、IP 白名单与有效期。明文只在创建时显示一次，网关只保存哈希。">
      <template #actions>
        <el-button :loading="store.loading" @click="store.load()">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
        <el-button type="primary" :disabled="!store.subscriptions.length" @click="openCreate">
          <Icon name="plus" :size="15" /><span class="btn-gap">创建 Key</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-input v-model="filterName" placeholder="搜索名称或前缀" clearable size="default" class="toolbar__search">
        <template #prefix><Icon name="search" :size="14" /></template>
      </el-input>
      <el-radio-group v-model="filterStatus" size="default">
        <el-radio-button value="">全部 {{ counts.all }}</el-radio-button>
        <el-radio-button value="active">生效 {{ counts.active }}</el-radio-button>
        <el-radio-button value="suspended">停用 {{ counts.suspended }}</el-radio-button>
        <el-radio-button value="revoked">吊销 {{ counts.revoked }}</el-radio-button>
      </el-radio-group>
      <span class="toolbar__spacer" />
      <span class="toolbar__note">
        共 <b class="mono">{{ filtered.length }}</b> 把 · 每订阅上限见套餐 <code class="mono">max_keys</code>
      </span>
    </div>

    <section v-if="store.loading && !store.keys.length" class="panel">
      <div class="panel__body">
        <div v-for="i in 5" :key="i" class="row-skeleton">
          <span class="sk" style="width: 22%; height: 12px" />
          <span class="sk" style="width: 14%; height: 12px" />
          <span class="sk" style="width: 9%; height: 12px" />
          <span class="sk" style="width: 12%; height: 12px" />
          <span class="sk" style="width: 16%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error && !store.keys.length" class="panel">
      <EmptyState icon="alert" title="Key 列表加载失败" :desc="error">
        <el-button size="small" @click="store.load()">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!store.keys.length" class="panel">
      <EmptyState icon="key" title="还没有创建任何 Key" desc="创建一把 Key 后即可在代码里调用 /v1/chat/completions。建议按环境（生产 / 测试）分别创建，便于单独吊销。">
        <el-button type="primary" size="small" @click="openCreate">创建第一把 Key</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="filtered" row-key="key_id" size="default" :row-class-name="rowClassName">
        <el-table-column label="名称" min-width="190">
          <template #default="{ row }">
            <div class="cell-name">
              <span class="cell-name__text">{{ row.name }}</span>
              <span class="cell-name__sub mono">#{{ row.key_id }} · sub {{ row.subscription_id }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="Key 前缀" min-width="210">
          <template #default="{ row }">
            <div class="cell-key">
              <code class="cell-key__code">{{ maskKey(row.key_prefix) }}</code>
              <CopyButton :value="row.key_prefix" label="前缀" />
            </div>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="104">
          <template #default="{ row }">
            <span class="badge" :class="`badge--${statusMeta(row.status).tone}`">{{ statusMeta(row.status).label }}</span>
          </template>
        </el-table-column>

        <el-table-column label="模型范围" min-width="180">
          <template #default="{ row }">
            <div class="chips">
              <span v-for="m in row.models.slice(0, 2)" :key="m" class="chip" :title="m">{{ modelLabel(m) }}</span>
              <span v-if="row.models.length > 2" class="chip chip--more">+{{ row.models.length - 2 }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="IP 白名单" width="112">
          <template #default="{ row }">
            <span v-if="row.ip_whitelist.length" class="mono tag-sm">{{ row.ip_whitelist.length }} 条</span>
            <span v-else class="dim">不限</span>
          </template>
        </el-table-column>

        <el-table-column label="最近使用" width="126">
          <template #default="{ row }">
            <span :title="row.last_used_at ? fmtDateTime(row.last_used_at) : ''">{{ fmtRelative(row.last_used_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="30 天用量" width="150" align="right" class-name="col-num">
          <template #default="{ row }">
            <div class="cell-usage">
              <span class="cell-usage__cost">{{ fmtMoney(row.usage_micros_30d) }}</span>
              <span class="cell-usage__req mono">{{ fmtTokens(row.requests_30d) }} 次</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="有效期至" width="112">
          <template #default="{ row }">
            <span class="mono" :class="{ dim: !row.expires_at }">{{ row.expires_at ? fmtDateTime(row.expires_at) : '永久' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="176" fixed="right" align="right">
          <template #default="{ row }">
            <div class="row-actions">
              <el-switch
                :model-value="row.status === 'active'"
                :disabled="row.status === 'revoked' || toggling === row.key_id"
                :loading="toggling === row.key_id"
                size="small"
                inline-prompt
                active-text="启"
                inactive-text="停"
                @change="(v: string | number | boolean) => onToggle(row, Boolean(v))"
              />
              <el-button text size="small" :disabled="row.status === 'revoked'" @click="openEdit(row)">编辑</el-button>
              <el-button text size="small" type="danger" :disabled="row.status === 'revoked'" @click="onRevoke(row)">吊销</el-button>
            </div>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- ============ create dialog ============ -->
    <el-dialog v-model="createOpen" title="创建 Key" width="580px" :close-on-click-modal="false" append-to-body>
      <el-alert type="info" :closable="false" show-icon style="margin-bottom: 18px">
        创建成功后明文只显示一次。网关只保存哈希，关闭弹窗后无法再次获取。
      </el-alert>

      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent>
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="例如：生产环境 / 客服机器人" maxlength="40" show-word-limit />
        </el-form-item>

        <el-form-item label="归属订阅" prop="subscription_id">
          <el-select v-model="form.subscription_id" placeholder="选择订阅" style="width: 100%">
            <el-option v-for="opt in subOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
          <div class="field-hint">额度从该订阅扣减；订阅停用后其下所有 Key 一并失效。</div>
        </el-form-item>

        <el-form-item label="模型范围" prop="models">
          <el-select v-model="form.models" multiple filterable collapse-tags collapse-tags-tooltip placeholder="选择可用模型" style="width: 100%">
            <el-option v-for="opt in modelOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
          <div class="field-hint">只允许调用选中的模型，其余返回 403 model_not_allowed。</div>
        </el-form-item>

        <el-form-item label="IP 白名单">
          <el-select v-model="form.ip_whitelist" multiple filterable allow-create default-first-option :reserve-keyword="false"
            placeholder="输入 CIDR 或 IP 后回车，如 1.2.3.0/24" style="width: 100%" />
          <div class="field-hint">留空表示不限来源 IP。支持单 IP 与 CIDR。</div>
        </el-form-item>

        <el-form-item label="有效期">
          <el-date-picker v-model="form.expires_at" type="datetime" placeholder="留空表示永久有效" style="width: 100%"
            value-format="YYYY-MM-DD HH:mm:ss" />
          <div class="field-hint">到期后 Key 自动失效并返回 401 key_expired。</div>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="createOpen = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="submitCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- ============ one-time plaintext modal ============ -->
    <el-dialog v-model="freshKeyVisible" title="Key 已创建" width="600px" :close-on-click-modal="false" :show-close="false" append-to-body
      @close="closeFresh">
      <div v-if="store.freshKey" class="fresh">
        <div class="fresh__banner">
          <Icon name="alert" :size="18" />
          <div>
            <strong>明文只显示这一次。</strong>
            <span>关闭后无法再次查看——网关只保存哈希。请立刻复制并存入密码管理器或环境变量。</span>
          </div>
        </div>

        <div class="fresh__key">
          <code class="fresh__value mono">{{ store.freshKey.key }}</code>
          <el-button type="primary" size="small" @click="copyFresh">
            <Icon name="copy" :size="14" /><span class="btn-gap">复制</span>
          </el-button>
        </div>

        <dl class="fresh__meta">
          <div><dt>名称</dt><dd>{{ store.freshKey.name }}</dd></div>
          <div><dt>前缀</dt><dd class="mono">{{ store.freshKey.key_prefix }}</dd></div>
          <div><dt>状态</dt><dd>{{ store.freshKey.status }}</dd></div>
          <div><dt>创建时间</dt><dd class="mono">{{ fmtDateTime(store.freshKey.created_at) }}</dd></div>
        </dl>
      </div>

      <template #footer>
        <el-button type="primary" @click="closeFresh">我已保存，关闭</el-button>
      </template>
    </el-dialog>

    <!-- ============ edit dialog ============ -->
    <el-dialog v-model="editOpen" title="编辑 Key" width="560px" append-to-body>
      <div v-if="editing" class="edit-head">
        <code class="mono">{{ maskKey(editing.key_prefix) }}</code>
        <span class="mono dim">#{{ editing.key_id }}</span>
      </div>

      <el-form label-position="top" @submit.prevent>
        <el-form-item label="名称">
          <el-input v-model="editForm.name" maxlength="40" show-word-limit />
        </el-form-item>
        <el-form-item label="模型范围">
          <el-select v-model="editForm.models" multiple filterable collapse-tags placeholder="选择可用模型" style="width: 100%">
            <el-option v-for="opt in modelOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="IP 白名单">
          <el-select v-model="editForm.ip_whitelist" multiple filterable allow-create default-first-option :reserve-keyword="false"
            placeholder="留空表示不限" style="width: 100%" />
        </el-form-item>
        <el-form-item label="有效期">
          <el-date-picker v-model="editForm.expires_at" type="datetime" placeholder="留空表示永久有效" style="width: 100%"
            value-format="YYYY-MM-DD HH:mm:ss" />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="editOpen = false">取消</el-button>
        <el-button type="primary" :loading="store.saving" @click="submitEdit">保存</el-button>
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

.toolbar__search {
  width: 240px;
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

/* ---- table cells ---- */
.cell-name {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.cell-name__text {
  color: var(--ink-1);
  font-weight: 550;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cell-name__sub {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.cell-key {
  display: flex;
  align-items: center;
  gap: var(--sp-1);
}

.cell-key__code {
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  color: var(--ink-2);
  letter-spacing: -0.02em;
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
  font-size: var(--fs-2xs);
  color: var(--ink-2);
  white-space: nowrap;
  max-width: 118px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.chip--more {
  color: var(--ink-3);
}

.tag-sm {
  font-size: var(--fs-2xs);
  color: var(--ink-2);
}

.cell-usage {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 0;
}

.cell-usage__cost {
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  color: var(--ink-1);
  font-weight: 550;
}

.cell-usage__req {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

:deep(.is-revoked) td {
  opacity: 0.62;
}

/* ---- field hint ---- */
.field-hint {
  margin-top: 4px;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  line-height: 1.5;
}

/* ---- fresh key modal ---- */
.fresh {
  display: flex;
  flex-direction: column;
  gap: var(--sp-4);
}

.fresh__banner {
  display: flex;
  gap: var(--sp-3);
  padding: var(--sp-3) var(--sp-4);
  border-radius: var(--r-sm);
  background: var(--danger-soft);
  border: 1px solid color-mix(in srgb, var(--danger) 34%, transparent);
  color: var(--danger);
  font-size: var(--fs-xs);
  line-height: 1.55;
}

.fresh__banner strong {
  font-weight: 700;
}

.fresh__banner strong + span {
  display: block;
  color: color-mix(in srgb, var(--danger) 82%, var(--ink-2));
}

.fresh__key {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  padding: var(--sp-3) var(--sp-4);
  border-radius: var(--r-sm);
  background: var(--bg-sunken);
  border: 1px solid var(--line-2);
}

.fresh__value {
  flex: 1;
  font-size: var(--fs-xs);
  color: var(--ink-1);
  word-break: break-all;
  line-height: 1.6;
  letter-spacing: -0.01em;
}

.fresh__meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--sp-3);
  margin: 0;
}

.fresh__meta dt {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.fresh__meta dd {
  margin: 1px 0 0;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}

.edit-head {
  display: flex;
  align-items: baseline;
  gap: var(--sp-3);
  padding: var(--sp-2) var(--sp-3);
  margin-bottom: var(--sp-4);
  border-radius: var(--r-sm);
  background: var(--bg-sunken);
  border: 1px solid var(--line-1);
  font-size: var(--fs-xs);
}
</style>
