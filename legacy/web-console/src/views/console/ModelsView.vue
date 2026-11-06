<script setup lang="ts">
/**
 * 模型列表 — what this account may call, and what it costs.
 *
 * The availability note is the point of this screen: "不在套餐内" models are
 * shown greyed with the reason, so a 403 from the gateway is never a surprise.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { consoleApi } from '@/api/console'
import type { ConsoleModel } from '@/types/api'
import { fmtInt, fmtMoney } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const models = ref<ConsoleModel[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const keyword = ref('')
const group = ref('')

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await consoleApi.models()
    models.value = res.items
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

/**
 * The contract's `/console/v1/models` only returns models this account may use,
 * so the gateway never tells us what was excluded. The list below is a static
 * hint of the catalogue entries that live in the `premium` group — it exists so
 * a customer who saw `gpt-4o` in a V10 product page understands the 403. It is
 * display-only and must be kept in sync with `group_name` on the admin side.
 */
const PREMIUM_HINT = [
  { model_name: 'gpt-4o', reason: 'premium 分组，专业版及以上可用' },
  { model_name: 'qwen-max', reason: 'premium 分组，专业版及以上可用' },
]

const unavailable = computed(() => {
  const owned = new Set(models.value.map((m) => m.model_name))
  return PREMIUM_HINT.filter((m) => !owned.has(m.model_name))
})

const CAP_LABEL: Record<string, string> = {
  vision: '视觉',
  tools: '工具调用',
  json: 'JSON 模式',
  reasoning: '深度推理',
  audio: '语音',
  cache: '缓存',
}

const capLabel = (c: string) => CAP_LABEL[c] ?? c

const fmtCtx = (n: number) => (n >= 1024 ? `${fmtInt(n / 1024)}K` : fmtInt(n))
</script>

<template>
  <div class="page">
    <PageHeader title="模型列表" desc="这里只显示当前套餐分组内可用的模型。倍率用于计算费用，单价为折算后的绝对价格（每百万 token）。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <div class="toolbar">
      <el-input v-model="keyword" placeholder="搜索模型名" clearable class="toolbar__search">
        <template #prefix><Icon name="search" :size="14" /></template>
      </el-input>
      <el-radio-group v-model="group" size="default">
        <el-radio-button value="">全部分组</el-radio-button>
        <el-radio-button v-for="g in groups" :key="g" :value="g">{{ g }}</el-radio-button>
      </el-radio-group>
      <span class="toolbar__spacer" />
      <span class="toolbar__note">共 <b class="mono">{{ filtered.length }}</b> 个可用模型</span>
    </div>

    <section v-if="loading" class="panel">
      <div class="panel__body">
        <div v-for="i in 5" :key="i" class="row-skeleton">
          <span class="sk" style="width: 20%; height: 12px" />
          <span class="sk" style="width: 10%; height: 12px" />
          <span class="sk" style="width: 24%; height: 12px" />
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
      <EmptyState icon="layers" title="没有匹配的模型" desc="换一个关键词，或清空分组筛选。">
        <el-button size="small" @click="keyword = ''; group = ''">重置筛选</el-button>
      </EmptyState>
    </section>

    <section v-else class="panel">
      <el-table :data="filtered" row-key="model_name">
        <el-table-column label="模型" min-width="230">
          <template #default="{ row }">
            <div class="cell-model">
              <span class="cell-model__name">{{ row.display_name }}</span>
              <code class="cell-model__id">{{ row.model_name }}</code>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="分组" width="110">
          <template #default="{ row }">
            <span class="mono group-tag">{{ row.group_name }}</span>
          </template>
        </el-table-column>

        <el-table-column label="上下文长度" width="130" align="right" class-name="col-num">
          <template #default="{ row }">
            <span class="mono">{{ fmtCtx(row.context_len) }}</span>
            <span class="unit">tok</span>
          </template>
        </el-table-column>

        <el-table-column label="能力" min-width="200">
          <template #default="{ row }">
            <div class="chips">
              <span v-for="c in row.capabilities" :key="c" class="chip">{{ capLabel(c) }}</span>
              <span v-if="!row.capabilities.length" class="dim">基础对话</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="倍率" width="152" align="right" class-name="col-num">
          <template #default="{ row }">
            <div class="ratios">
              <span class="ratios__main mono">×{{ row.ratio.model_ratio }}</span>
              <span class="ratios__sub mono">补全 ×{{ row.ratio.completion_ratio }} · 缓存 {{ row.ratio.cached_ratio }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="单价（输入 / 输出 每 1M）" width="196" align="right" class-name="col-num">
          <template #default="{ row }">
            <div class="price">
              <span class="price__in mono">{{ fmtMoney(row.price_hint.input_micros_per_1m) }}</span>
              <span class="price__sep">/</span>
              <span class="price__out mono">{{ fmtMoney(row.price_hint.output_micros_per_1m) }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="可用性" width="128">
          <template #default>
            <span class="badge badge--ok">可用</span>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <section v-if="unavailable.length && !loading" class="unavailable">
      <div class="unavailable__head">
        <Icon name="info" :size="15" />
        <span>以下模型不在当前套餐的模型分组内，调用会返回 <code class="mono">403 model_not_allowed</code></span>
      </div>
      <ul class="unavailable__list">
        <li v-for="u in unavailable" :key="u.model_name">
          <code class="mono">{{ u.model_name }}</code>
          <span class="dim">{{ u.reason }}</span>
        </li>
      </ul>
    </section>
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
  width: 220px;
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

.unit {
  margin-left: 3px;
  font-size: var(--fs-2xs);
  color: var(--ink-4);
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

.ratios {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
}

.ratios__main {
  font-size: var(--fs-sm);
  color: var(--ink-1);
  font-weight: 600;
}

.ratios__sub {
  font-size: var(--fs-2xs);
  color: var(--ink-4);
}

.price {
  display: flex;
  align-items: baseline;
  justify-content: flex-end;
  gap: 3px;
}

.price__in {
  color: var(--ink-1);
  font-size: var(--fs-xs);
}

.price__sep {
  color: var(--ink-4);
}

.price__out {
  color: var(--ink-2);
  font-size: var(--fs-xs);
}

.unavailable {
  border: 1px solid var(--line-1);
  border-left: 2px solid var(--ink-4);
  border-radius: var(--r-md);
  background: var(--bg-panel);
  padding: var(--sp-4) var(--sp-5);
}

.unavailable__head {
  display: flex;
  align-items: center;
  gap: var(--sp-2);
  font-size: var(--fs-xs);
  color: var(--ink-3);
}

.unavailable__list {
  margin: var(--sp-3) 0 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.unavailable__list li {
  display: flex;
  align-items: baseline;
  gap: var(--sp-3);
  font-size: var(--fs-xs);
}
</style>
