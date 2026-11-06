<script setup lang="ts">
/**
 * 套餐 — plan catalogue.
 *
 * Plans are what V10 sells, so every field here maps to a commercial promise:
 * quota_tokens (额度), period (周期), price_cents (售价), max_keys (Key 上限).
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { Plan, PlanPeriod } from '@/types/api'
import { fmtInt, fmtTokens, toYuan } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const plans = ref<Plan[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    plans.value = await adminApi.listPlans()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载套餐失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const PERIOD_LABEL: Record<PlanPeriod, string> = {
  month: '月',
  quarter: '季',
  year: '年',
  once: '一次性',
}

const periodLabel = (p: PlanPeriod | string) => PERIOD_LABEL[p as PlanPeriod] ?? String(p)

/* ------------------------------------------------------------------ */
/* Dialog                                                             */
/* ------------------------------------------------------------------ */

const dialogOpen = ref(false)
const mode = ref<'create' | 'edit'>('create')
const saving = ref(false)
const originalCode = ref<string | null>(null)

const blank = (): Plan => ({
  plan_code: '',
  name: '',
  quota_tokens: 5_000_000,
  period: 'month',
  rpm_limit: 60,
  tpm_limit: 120_000,
  concurrency: 5,
  model_group: 'default',
  max_keys: 5,
  price_cents: 2900,
  group_ratio: 1,
  status: 1,
})

const form = reactive<Plan>(blank())
const formRef = ref<{ validate: () => Promise<boolean> } | null>(null)

const rules = {
  plan_code: [
    { required: true, message: '套餐编码必填（V10 下单时使用）', trigger: 'blur' },
    { pattern: /^[a-z0-9-]+$/, message: '只允许小写字母、数字与连字符', trigger: 'blur' },
  ],
  name: [{ required: true, message: '套餐名称必填', trigger: 'blur' }],
}

/** price_cents is cents; show 元 for editing convenience. */
const priceYuan = computed({
  get: () => form.price_cents / 100,
  set: (v: number) => {
    form.price_cents = Math.round((v || 0) * 100)
  },
})

function openCreate() {
  Object.assign(form, blank())
  originalCode.value = null
  mode.value = 'create'
  dialogOpen.value = true
}

function openEdit(row: Plan) {
  Object.assign(form, JSON.parse(JSON.stringify(row)) as Plan)
  originalCode.value = row.plan_code
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
    if (mode.value === 'edit' && originalCode.value) {
      await adminApi.updatePlan(originalCode.value, { ...form })
      ElMessage.success('套餐已更新')
    } else {
      await adminApi.upsertPlan({ ...form })
      ElMessage.success('套餐已保存（按 plan_code upsert）')
    }
    dialogOpen.value = false
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    saving.value = false
  }
}

async function toggleStatus(row: Plan) {
  try {
    await adminApi.updatePlan(row.plan_code, { status: row.status === 1 ? 0 : 1 })
    ElMessage.success(row.status === 1 ? '套餐已下架' : '套餐已上架')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '操作失败')
  }
}

async function remove(row: Plan) {
  try {
    await ElMessageBox.confirm(
      `删除套餐「${row.name}」后，已存在的订阅不受影响，但 V10 将无法再按该编码下单。确认删除？`,
      '确认删除',
      { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }
  try {
    await adminApi.deletePlan(row.plan_code)
    ElMessage.success('套餐已删除')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '删除失败')
  }
}
</script>

<template>
  <div class="page">
    <PageHeader title="套餐" desc="套餐由 V10 售卖，网关只负责按编码开通与计量。额度单位为 token，售价按分存储（100 分 = 1 元）。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
        <el-button type="primary" @click="openCreate">
          <Icon name="plus" :size="15" /><span class="btn-gap">新建套餐</span>
        </el-button>
      </template>
    </PageHeader>

    <section v-if="loading" class="panel">
      <div class="panel__body">
        <div v-for="i in 4" :key="i" class="row-skeleton">
          <span class="sk" style="width: 16%; height: 12px" />
          <span class="sk" style="width: 12%; height: 12px" />
          <span class="sk" style="width: 20%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error" class="panel">
      <EmptyState icon="alert" title="套餐加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!plans.length" class="panel">
      <EmptyState icon="shield" title="还没有配置任何套餐" desc="套餐是 V10 商品与网关额度之间的桥梁，先建一个再让 V10 下单。">
        <el-button type="primary" size="small" @click="openCreate">新建套餐</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="plans" row-key="plan_code">
        <el-table-column label="套餐" min-width="196">
          <template #default="{ row }">
            <div class="cell-plan">
              <span class="cell-plan__name">{{ row.name }}</span>
              <code class="cell-plan__code">{{ row.plan_code }}</code>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="周期额度" width="140" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono">{{ fmtTokens(row.quota_tokens) }}</span>
            <span class="unit">tok</span>
          </template>
        </el-table-column>

        <el-table-column label="周期" width="90">
          <template #default="{ row }">
            <span class="badge badge--idle badge--no-dot">{{ periodLabel(row.period) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="限流（RPM / TPM）" width="176" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono">{{ fmtInt(row.rpm_limit) }}</span>
            <span class="dim"> / </span>
            <span class="mono">{{ fmtInt(row.tpm_limit) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="并发" width="80" align="right" class-name="col-num">
          <template #default="{ row }"><span class="mono">{{ row.concurrency }}</span></template>
        </el-table-column>

        <el-table-column label="模型分组" width="150">
          <template #default="{ row }"><code class="mono group-tag">{{ row.model_group }}</code></template>
        </el-table-column>

        <el-table-column label="Key 上限" width="96" align="right" class-name="col-num">
          <template #default="{ row }"><span class="mono">{{ row.max_keys }}</span></template>
        </el-table-column>

        <el-table-column label="分组倍率" width="102" align="right" class-name="col-num">
          <template #default="{ row }"><span class="mono">×{{ row.group_ratio }}</span></template>
        </el-table-column>

        <el-table-column label="售价" width="112" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono price">¥{{ toYuan(row.price_cents * 10_000) }}</span>
            <span class="dim mono price-sub">{{ row.price_cents }} 分</span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <span class="badge" :class="row.status === 1 ? 'badge--ok' : 'badge--idle'">{{ row.status === 1 ? '在售' : '下架' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="176" fixed="right" align="right">
          <template #default="{ row }">
            <div class="row-actions">
              <el-button text size="small" @click="toggleStatus(row)">{{ row.status === 1 ? '下架' : '上架' }}</el-button>
              <el-button text size="small" @click="openEdit(row)">编辑</el-button>
              <el-button text size="small" type="danger" @click="remove(row)">删除</el-button>
            </div>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- ============ dialog ============ -->
    <el-dialog v-model="dialogOpen" :title="mode === 'create' ? '新建套餐' : `编辑套餐 ${originalCode}`" width="640px"
      append-to-body>
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent>
        <div class="form-grid">
          <el-form-item label="套餐编码（plan_code）" prop="plan_code">
            <el-input v-model="form.plan_code" :disabled="mode === 'edit'" class="mono-input" placeholder="pro" />
          </el-form-item>
          <el-form-item label="套餐名称" prop="name">
            <el-input v-model="form.name" placeholder="专业版" />
          </el-form-item>

          <el-form-item label="周期额度（token）">
            <el-input-number v-model="form.quota_tokens" :min="0" :step="1_000_000" controls-position="right" style="width: 100%" />
          </el-form-item>
          <el-form-item label="计费周期">
            <el-select v-model="form.period" style="width: 100%">
              <el-option label="月" value="month" />
              <el-option label="季" value="quarter" />
              <el-option label="年" value="year" />
              <el-option label="一次性" value="once" />
            </el-select>
          </el-form-item>

          <el-form-item label="RPM 上限">
            <el-input-number v-model="form.rpm_limit" :min="0" :step="10" controls-position="right" style="width: 100%" />
          </el-form-item>
          <el-form-item label="TPM 上限">
            <el-input-number v-model="form.tpm_limit" :min="0" :step="10_000" controls-position="right" style="width: 100%" />
          </el-form-item>

          <el-form-item label="并发上限">
            <el-input-number v-model="form.concurrency" :min="1" :step="1" controls-position="right" style="width: 100%" />
          </el-form-item>
          <el-form-item label="每订阅 Key 上限">
            <el-input-number v-model="form.max_keys" :min="1" :step="1" controls-position="right" style="width: 100%" />
          </el-form-item>

          <el-form-item label="可用模型分组">
            <el-input v-model="form.model_group" class="mono-input" placeholder="default 或 default+premium" />
          </el-form-item>
          <el-form-item label="分组倍率">
            <el-input-number v-model="form.group_ratio" :min="0" :max="5" :step="0.05" :precision="3" controls-position="right" style="width: 100%" />
          </el-form-item>

          <el-form-item label="售价（元）">
            <el-input-number v-model="priceYuan" :min="0" :step="10" :precision="2" controls-position="right" style="width: 100%" />
            <div class="hint">保存为 {{ form.price_cents }} 分</div>
          </el-form-item>
          <el-form-item label="状态">
            <el-switch v-model="form.status" :active-value="1" :inactive-value="0" inline-prompt active-text="在售" inactive-text="下架" />
          </el-form-item>
        </div>
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

.row-skeleton {
  display: flex;
  gap: var(--sp-6);
  padding: 12px 0;
  border-bottom: 1px solid var(--line-1);
}

.cell-plan {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.cell-plan__name {
  color: var(--ink-1);
  font-weight: 550;
}

.cell-plan__code {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.unit {
  margin-left: 3px;
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

.price {
  display: block;
  color: var(--ink-1);
  font-weight: 550;
}

.price-sub {
  display: block;
  font-size: var(--fs-2xs);
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
}

:deep(.mono-input .el-input__inner) {
  font-family: var(--font-mono);
}
</style>
