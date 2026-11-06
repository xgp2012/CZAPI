<script setup lang="ts">
/**
 * 接入文档 — copy-paste quickstart built from the gateway-provided base_url.
 *
 * The base_url is injected into every example at render time, so the snippets
 * can never drift from the deployment they describe.
 */
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { consoleApi } from '@/api/console'
import type { DocsPayload } from '@/types/api'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import CopyButton from '@/components/CopyButton.vue'
import Icon from '@/components/Icon.vue'

const data = ref<DocsPayload | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const tab = ref<'curl' | 'python' | 'node' | 'langchain'>('curl')

async function load() {
  loading.value = true
  error.value = null
  try {
    data.value = await consoleApi.docs()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载文档失败'
    ElMessage.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const tabs = [
  { key: 'curl', label: 'curl' },
  { key: 'python', label: 'Python' },
  { key: 'node', label: 'Node' },
  { key: 'langchain', label: 'LangChain' },
] as const

const code = computed(() => (data.value ? data.value.examples[tab.value] : ''))

const chatUrl = computed(() => (data.value ? `${data.value.base_url}/v1/chat/completions` : ''))

const facts = computed(() => [
  { k: 'Base URL', v: data.value?.base_url ?? '—', mono: true },
  { k: 'Chat 端点', v: chatUrl.value || '—', mono: true },
  { k: '认证方式', v: 'Authorization: Bearer sk-…', mono: true },
  { k: '协议', v: 'OpenAI 兼容（/v1/chat/completions, /v1/models）', mono: false },
])
</script>

<template>
  <div class="page">
    <PageHeader title="接入文档" desc="CZAPI 兼容 OpenAI 协议，任何支持自定义 base_url 的 SDK 都可以直接接入。">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <Icon name="refresh" :size="15" /><span class="btn-gap">刷新</span>
        </el-button>
        <RouterLink to="/keys">
          <el-button type="primary">
            <Icon name="key" :size="15" /><span class="btn-gap">去创建 Key</span>
          </el-button>
        </RouterLink>
      </template>
    </PageHeader>

    <div v-if="loading" class="panel">
      <div class="panel__body stack">
        <span class="sk" style="width: 40%; height: 14px" />
        <span class="sk" style="width: 100%; height: 160px" />
      </div>
    </div>

    <div v-else-if="error" class="panel">
      <EmptyState icon="alert" title="文档加载失败" :desc="error">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </div>

    <template v-else-if="data">
      <section class="panel">
        <div class="panel__head"><span class="panel__title">连接信息</span></div>
        <dl class="facts">
          <div v-for="f in facts" :key="f.k" class="facts__row">
            <dt>{{ f.k }}</dt>
            <dd :class="{ mono: f.mono }">{{ f.v }}</dd>
            <CopyButton v-if="f.mono" :value="f.v" label="" />
          </div>
        </dl>
      </section>

      <section class="panel">
        <div class="panel__head">
          <span class="panel__title">最小可用示例</span>
          <span class="panel__hint">所有示例中的 base_url 与端点均由网关下发</span>
        </div>

        <div class="tabs">
          <button v-for="t in tabs" :key="t.key" type="button" class="tab" :class="{ 'is-active': tab === t.key }"
            @click="tab = t.key">
            {{ t.label }}
          </button>
        </div>

        <div class="panel__body">
          <div class="codeblock">
            <pre><code>{{ code }}</code></pre>
            <div class="codeblock__copy"><CopyButton :value="code" label="复制代码" /></div>
          </div>

          <div class="note">
            <Icon name="info" :size="15" />
            <p>
              Key 在 <RouterLink to="/keys">Key 管理</RouterLink> 页面创建。明文只在创建时显示一次，
              请立刻存入环境变量（如 <code class="mono">CZAPI_KEY</code>），不要写进代码仓库。
            </p>
          </div>
        </div>
      </section>

      <section class="panel">
        <div class="panel__head"><span class="panel__title">常见错误</span></div>
        <div class="panel__body panel__body--flush">
          <table class="errs">
            <thead>
              <tr><th>状态码</th><th>场景</th><th>处理方式</th></tr>
            </thead>
            <tbody>
              <tr><td class="mono">401</td><td>Key 无效、被吊销或已过期</td><td>在 Key 管理确认状态，必要时重新创建</td></tr>
              <tr><td class="mono">403</td><td>模型不在该 Key 的允许范围内，或 IP 不在白名单</td><td>编辑 Key 的模型范围 / IP 白名单</td></tr>
              <tr><td class="mono">402</td><td>订阅额度与余额均不足</td><td>在 V10 充值，或联系管理员调账</td></tr>
              <tr><td class="mono">429</td><td>触发 RPM / 并发限制</td><td>降低并发或申请更高套餐</td></tr>
              <tr><td class="mono">503</td><td>网关维护中（总开关关闭）或上游全熔断</td><td>查看状态页或联系运维</td></tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.btn-gap {
  margin-left: 6px;
}

.facts {
  margin: 0;
  padding: 0;
}

.facts__row {
  display: grid;
  grid-template-columns: 140px minmax(0, 1fr) 64px;
  align-items: center;
  gap: var(--sp-3);
  padding: 10px var(--sp-5);
  border-bottom: 1px solid var(--line-1);
}

.facts__row:last-child {
  border-bottom: none;
}

.facts__row dt {
  font-size: var(--fs-xs);
  color: var(--ink-4);
}

.facts__row dd {
  margin: 0;
  font-size: var(--fs-xs);
  color: var(--ink-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tabs {
  display: flex;
  gap: 2px;
  padding: var(--sp-2) var(--sp-4) 0;
  border-bottom: 1px solid var(--line-1);
}

.tab {
  appearance: none;
  border: none;
  background: transparent;
  padding: 8px var(--sp-4);
  font-size: var(--fs-sm);
  font-weight: 550;
  color: var(--ink-3);
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  border-radius: var(--r-sm) var(--r-sm) 0 0;
}

.tab:hover {
  color: var(--ink-1);
  background: var(--bg-hover);
}

.tab.is-active {
  color: var(--ink-1);
  border-bottom-color: var(--accent);
}

.note {
  display: flex;
  gap: var(--sp-3);
  margin-top: var(--sp-4);
  padding: var(--sp-3) var(--sp-4);
  border-radius: var(--r-sm);
  background: var(--info-soft);
  border: 1px solid color-mix(in srgb, var(--info) 24%, transparent);
  color: var(--info);
}

.note p {
  font-size: var(--fs-xs);
  line-height: 1.6;
  color: var(--ink-2);
}

.errs {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--fs-xs);
}

.errs th {
  text-align: left;
  font-weight: 600;
  font-size: var(--fs-2xs);
  color: var(--ink-3);
  padding: 9px var(--sp-5);
  background: var(--bg-sunken);
  border-bottom: 1px solid var(--line-2);
}

.errs td {
  padding: 10px var(--sp-5);
  border-bottom: 1px solid var(--line-1);
  color: var(--ink-2);
}

.errs tr:last-child td {
  border-bottom: none;
}
</style>
