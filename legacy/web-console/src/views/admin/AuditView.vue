<script setup lang="ts">
/**
 * 审计日志 — who changed what, when, from where.
 *
 * Read-only by design. The detail cell is raw JSON, so it gets the mono face and
 * a horizontal scroll rather than being prettified into a false structure.
 */
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { adminApi } from '@/api/admin'
import type { AuditEntry } from '@/types/api'
import { fmtDateTime, fmtInt } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import Icon from '@/components/Icon.vue'

const rows = ref<AuditEntry[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await adminApi.listAudit(page.value, pageSize.value)
    rows.value = res.items
    total.value = res.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载审计日志失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <PageHeader title="审计日志" desc="所有后台写操作都会留痕：操作人、对象、前后值与来源 IP。日志只读，保留 365 天。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <section v-if="loading && !rows.length" class="panel">
      <div class="panel__body">
        <div v-for="i in 8" :key="i" class="row-skeleton">
          <span class="sk" style="width: 12%; height: 12px" />
          <span class="sk" style="width: 18%; height: 12px" />
          <span class="sk" style="width: 34%; height: 12px" />
        </div>
      </div>
    </section>

    <section v-else-if="error && !rows.length" class="panel">
      <EmptyState icon="alert" title="审计日志加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </section>

    <section v-else-if="!rows.length" class="panel">
      <EmptyState icon="scroll" title="暂无审计记录" desc="后台的写操作（调账、改配置、封禁等）会出现在这里。" />
    </section>

    <section v-else class="panel">
      <el-table :data="rows" row-key="id">
        <el-table-column label="时间" width="140">
          <template #default="{ row }"><span class="mono cell-time">{{ fmtDateTime(row.created_at) }}</span></template>
        </el-table-column>

        <el-table-column label="操作人" width="180">
          <template #default="{ row }"><span class="mono actor">{{ row.actor }}</span></template>
        </el-table-column>

        <el-table-column label="动作" width="160">
          <template #default="{ row }">
            <span class="action">{{ row.action }}</span>
          </template>
        </el-table-column>

        <el-table-column label="对象" width="160">
          <template #default="{ row }"><code class="mono target">{{ row.target }}</code></template>
        </el-table-column>

        <el-table-column label="明细" min-width="280">
          <template #default="{ row }">
            <code class="detail">{{ row.detail }}</code>
          </template>
        </el-table-column>

        <el-table-column label="来源 IP" width="140">
          <template #default="{ row }"><span class="mono dim">{{ row.ip }}</span></template>
        </el-table-column>

        <el-table-column label="ID" width="88" align="right" class-name="col-num">
          <template #default="{ row }"><span class="mono dim">{{ fmtInt(row.id) }}</span></template>
        </el-table-column>
      </el-table>

      <el-pagination :current-page="page" :page-size="pageSize" :total="total" :page-sizes="[20, 50, 100]"
        layout="total, sizes, prev, pager, next" background
        @current-change="(p: number) => { page = p; load() }" @size-change="(s: number) => { pageSize = s; page = 1; load() }" />
    </section>
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

.cell-time {
  font-size: var(--fs-xs);
  color: var(--ink-2);
}

.actor {
  font-size: var(--fs-xs);
  color: var(--ink-1);
}

.action {
  font-size: var(--fs-xs);
  color: var(--ink-2);
  font-weight: 550;
}

.target {
  font-size: var(--fs-2xs);
  color: var(--ink-3);
}

.detail {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--ink-4);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
