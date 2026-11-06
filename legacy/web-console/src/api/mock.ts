/**
 * In-browser mock of the whole frozen contract (docs/console-api-v1.md).
 *
 * State lives in a module-level object so mutations survive navigation within a
 * session: creating a key, revoking it, toggling a flag, adjusting a balance and
 * registering a site all behave the way the real gateway would.
 *
 * Determinism: every generator is seeded, so the 30-day trends and ledger rows
 * do not reshuffle on remount.
 */

import type {
  Account,
  AccountDetail,
  AdjustAccountResponse,
  AdminKey,
  AdminModel,
  AdminSubscription,
  AdminTrendPoint,
  ApiKey,
  AuditEntry,
  Channel,
  ChannelSyncResult,
  ChannelTestResult,
  ConsoleModel,
  CreateKeyRequest,
  CreateKeyResponse,
  Dashboard,
  DocsPayload,
  FlagValue,
  LedgerEntry,
  LedgerType,
  LoginResponse,
  Overview,
  Paged,
  Plan,
  ReconcileReport,
  Site,
  SiteCreateRequest,
  SiteCreateResponse,
  SiteRotateResponse,
  SubscriptionActionResponse,
  SubscriptionSummary,
  SystemSettings,
  TrendPoint,
  UsageLog,
  UsageLogQuery,
  UsageSummary,
  UsageTotals,
  ChannelKeySlotWrite,
  ChannelWrite,
} from '@/types/api'

/* ================================================================== */
/* Deterministic PRNG                                                  */
/* ================================================================== */

function mulberry32(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

const rnd = mulberry32(20261105)
const pick = <T,>(arr: readonly T[]): T => arr[Math.floor(rnd() * arr.length)]!
const between = (min: number, max: number) => Math.floor(min + rnd() * (max - min + 1))

const DAY = 86400
const nowSec = () => Math.floor(Date.now() / 1000)
const todayStart = () => {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  return Math.floor(d.getTime() / 1000)
}

function dayKey(offsetFromToday: number): string {
  const d = new Date((todayStart() + offsetFromToday * DAY) * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/** Simulated network latency so skeletons and loading states are visible. */
async function lag<T>(value: T, ms = 220): Promise<T> {
  await sleep(ms + Math.floor(rnd() * 130))
  return value
}

/* ================================================================== */
/* Static reference data                                               */
/* ================================================================== */

const MODEL_CATALOG = [
  {
    model_name: 'deepseek-chat',
    display_name: 'DeepSeek Chat',
    group_name: 'default',
    context_len: 65536,
    capabilities: ['tools', 'json'],
    model_ratio: 0.5,
    completion_ratio: 2.0,
    cached_ratio: 0.5,
    input_micros: 500_000,
    output_micros: 1_000_000,
  },
  {
    model_name: 'deepseek-reasoner',
    display_name: 'DeepSeek Reasoner',
    group_name: 'default',
    context_len: 65536,
    capabilities: ['reasoning', 'json'],
    model_ratio: 1.0,
    completion_ratio: 4.0,
    cached_ratio: 0.25,
    input_micros: 1_000_000,
    output_micros: 4_000_000,
  },
  {
    model_name: 'gpt-4o-mini',
    display_name: 'GPT-4o mini',
    group_name: 'default',
    context_len: 131072,
    capabilities: ['vision', 'tools', 'json'],
    model_ratio: 0.375,
    completion_ratio: 4.0,
    cached_ratio: 0.5,
    input_micros: 375_000,
    output_micros: 1_500_000,
  },
  {
    model_name: 'gpt-4o',
    display_name: 'GPT-4o',
    group_name: 'premium',
    context_len: 131072,
    capabilities: ['vision', 'tools', 'json'],
    model_ratio: 6.25,
    completion_ratio: 4.0,
    cached_ratio: 0.5,
    input_micros: 6_250_000,
    output_micros: 25_000_000,
  },
  {
    model_name: 'qwen-plus',
    display_name: '通义千问 Plus',
    group_name: 'default',
    context_len: 131072,
    capabilities: ['tools', 'json'],
    model_ratio: 0.4,
    completion_ratio: 3.0,
    cached_ratio: 0.4,
    input_micros: 400_000,
    output_micros: 1_200_000,
  },
  {
    model_name: 'qwen-max',
    display_name: '通义千问 Max',
    group_name: 'premium',
    context_len: 32768,
    capabilities: ['tools'],
    model_ratio: 2.0,
    completion_ratio: 3.0,
    cached_ratio: 0.4,
    input_micros: 2_000_000,
    output_micros: 6_000_000,
  },
  {
    model_name: 'glm-4-flash',
    display_name: 'GLM-4 Flash',
    group_name: 'default',
    context_len: 131072,
    capabilities: ['tools', 'json'],
    model_ratio: 0.05,
    completion_ratio: 1.0,
    cached_ratio: 0.5,
    input_micros: 50_000,
    output_micros: 50_000,
  },
  {
    model_name: 'moonshot-v1-8k',
    display_name: 'Kimi 8K',
    group_name: 'default',
    context_len: 8192,
    capabilities: ['json'],
    model_ratio: 1.2,
    completion_ratio: 1.0,
    cached_ratio: 0.5,
    input_micros: 1_200_000,
    output_micros: 1_200_000,
  },
] as const

const ALL_MODEL_NAMES = MODEL_CATALOG.map((m) => m.model_name)

const PLAN_SEED = [
  {
    plan_code: 'free',
    name: '体验版',
    quota_tokens: 1_000_000,
    period: 'month' as const,
    rpm_limit: 20,
    tpm_limit: 40_000,
    concurrency: 2,
    model_group: 'default',
    max_keys: 2,
    price_cents: 0,
    group_ratio: 1.0,
    status: 1,
  },
  {
    plan_code: 'basic',
    name: '基础版',
    quota_tokens: 5_000_000,
    period: 'month' as const,
    rpm_limit: 60,
    tpm_limit: 120_000,
    concurrency: 5,
    model_group: 'default',
    max_keys: 5,
    price_cents: 2900,
    group_ratio: 1.0,
    status: 1,
  },
  {
    plan_code: 'pro',
    name: '专业版',
    quota_tokens: 10_000_000,
    period: 'month' as const,
    rpm_limit: 300,
    tpm_limit: 600_000,
    concurrency: 20,
    model_group: 'default+premium',
    max_keys: 10,
    price_cents: 9900,
    group_ratio: 0.95,
    status: 1,
  },
  {
    plan_code: 'enterprise',
    name: '企业版',
    quota_tokens: 100_000_000,
    period: 'month' as const,
    rpm_limit: 2000,
    tpm_limit: 4_000_000,
    concurrency: 120,
    model_group: 'default+premium',
    max_keys: 50,
    price_cents: 99900,
    group_ratio: 0.85,
    status: 1,
  },
  {
    plan_code: 'legacy-once',
    name: '一次性包量（停售）',
    quota_tokens: 3_000_000,
    period: 'once' as const,
    rpm_limit: 30,
    tpm_limit: 60_000,
    concurrency: 3,
    model_group: 'default',
    max_keys: 3,
    price_cents: 1900,
    group_ratio: 1.0,
    status: 0,
  },
]

/* ================================================================== */
/* Seeded state                                                        */
/* ================================================================== */

const EMAIL_DOMAINS = ['example.com', 'acme.cn', 'studio.dev', 'northwind.io', 'lumen.ai', 'shopv10.cn']
const SURNAMES = ['lin', 'chen', 'wang', 'zhao', 'liu', 'sun', 'he', 'guo', 'zhou', 'wu']
const GIVEN = ['kai', 'yan', 'ming', 'jing', 'bo', 'xin', 'tao', 'lei', 'rui', 'hui']

interface MockStore {
  balanceMicros: number
  accounts: Account[]
  keys: ApiKey[]
  adminSubscriptions: AdminSubscription[]
  ledger: LedgerEntry[]
  channels: Channel[]
  models: AdminModel[]
  plans: Plan[]
  flags: FlagValue[]
  audit: AuditEntry[]
  sites: (Site & { secret_full: string })[]
  seq: number
}

function buildStore(): MockStore {
  const accounts: Account[] = []
  const total = 26
  for (let i = 0; i < total; i++) {
    const emailIndex = i === 0 ? 0 : between(0, 40)
    const local = i === 0 ? 'ops' : `${pick(SURNAMES)}${pick(GIVEN)}${emailIndex}`
    const domain = i === 0 ? EMAIL_DOMAINS[0]! : pick(EMAIL_DOMAINS)
    accounts.push({
      account_id: 70 + i,
      v10_client_id: 1000 + i * 7 + between(0, 5),
      email: `${local}@${domain}`,
      status: i % 11 === 7 ? 0 : 1,
      allow_negative: i % 6 === 0,
      balance_micros: i === 0 ? 50_000_000 : between(0, 320) * 1_000_000 + between(0, 999_999),
      created_at: nowSec() - between(10, 400) * DAY,
    })
  }

  const keys: ApiKey[] = []
  const names = ['生产环境', '测试 Key', '预发布', '内部脚本', 'V10 插件', '数据清洗', '客服机器人', 'Mobile App']
  for (let i = 0; i < 17; i++) {
    const status: ApiKey['status'] = i % 9 === 5 ? 'suspended' : i % 13 === 9 ? 'revoked' : 'active'
    const created = nowSec() - between(3, 200) * DAY
    keys.push({
      key_id: 300 + i,
      key_prefix: `sk-${Math.random().toString(36).slice(2, 10)}`,
      name: i === 0 ? '生产环境' : `${pick(names)}${i > 7 ? ' ' + (i - 6) : ''}`,
      status,
      subscription_id: 1 + (i % 3),
      v10_host_id: 550 + (i % 4),
      models: i % 5 === 0 ? [...ALL_MODEL_NAMES.slice(0, 2)] : [pick(ALL_MODEL_NAMES)],
      ip_whitelist: i % 4 === 0 ? ['1.2.3.0/24', '10.0.0.0/8'] : [],
      expires_at: i % 6 === 0 ? nowSec() + between(20, 200) * DAY : null,
      last_used_at: status === 'revoked' ? nowSec() - between(20, 60) * DAY : nowSec() - between(0, 3) * DAY,
      created_at: created,
      usage_micros_30d: status === 'revoked' ? between(0, 4_000_000) : between(200_000, 48_000_000),
      requests_30d: status === 'revoked' ? between(0, 40) : between(20, 4200),
    })
  }
  // Prefixes must look plausible; regenerate with the seeded generator for stability.
  const keyPrefixRnd = mulberry32(77)
  keys.forEach((k) => {
    const s = Math.floor(keyPrefixRnd() * 1e12).toString(36)
    k.key_prefix = `sk-${s.padEnd(8, 'x').slice(0, 8)}`
  })

  const firstAcc = accounts[0]!
  const subscriptionSeeds: SubscriptionSummary[] = [
    {
      subscription_id: 1,
      v10_host_id: 555,
      plan_code: 'pro',
      plan_name: '专业版',
      status: 'active',
      billing_mode: 'hybrid',
      period_start: todayStart() - 12 * DAY,
      period_end: todayStart() + 18 * DAY,
      quota_total: 10_000_000,
      quota_used: 2_500_000,
      quota_remaining: 7_500_000,
    },
    {
      subscription_id: 2,
      v10_host_id: 812,
      plan_code: 'basic',
      plan_name: '基础版',
      status: 'active',
      billing_mode: 'subscription',
      period_start: todayStart() - 26 * DAY,
      period_end: todayStart() + 4 * DAY,
      quota_total: 5_000_000,
      quota_used: 4_100_000,
      quota_remaining: 900_000,
    },
    {
      subscription_id: 3,
      v10_host_id: 903,
      plan_code: 'enterprise',
      plan_name: '企业版',
      status: 'suspended',
      billing_mode: 'balance',
      period_start: todayStart() - 5 * DAY,
      period_end: todayStart() + 25 * DAY,
      quota_total: 100_000_000,
      quota_used: 9_800_000,
      quota_remaining: 90_200_000,
    },
  ]

  const adminSubscriptions: AdminSubscription[] = []
  accounts.forEach((acc, i) => {
    const count = i === 0 ? 3 : between(0, 2)
    for (let j = 0; j < count; j++) {
      const isOwner = i === 0
      const seed = isOwner ? subscriptionSeeds[j]! : subscriptionSeeds[between(0, 2)]!
      const qTotal = isOwner ? seed.quota_total : between(1_000_000, 30_000_000)
      const qUsed = isOwner ? seed.quota_used : between(0, qTotal)
      const status: AdminSubscription['status'] =
        i % 13 === 4 ? 'suspended' : i % 17 === 8 ? 'expired' : 'active'
      adminSubscriptions.push({
        subscription_id: isOwner ? seed.subscription_id : 100 + i * 3 + j,
        account_id: acc.account_id,
        v10_client_id: acc.v10_client_id,
        v10_host_id: isOwner ? seed.v10_host_id : 500 + i * 11 + j,
        plan_code: seed.plan_code,
        plan_name: seed.plan_name,
        status,
        billing_mode: seed.billing_mode,
        period_start: todayStart() - between(1, 28) * DAY,
        period_end: todayStart() + between(1, 29) * DAY,
        quota_total: qTotal,
        quota_used: qUsed,
        quota_remaining: Math.max(0, qTotal - qUsed),
        max_keys: seed.plan_code === 'enterprise' ? 50 : seed.plan_code === 'pro' ? 10 : 5,
        created_at: nowSec() - between(10, 300) * DAY,
      })
    }
  })
  // The first three subscriptions must match the user-centre seeds exactly.
  adminSubscriptions[0] = {
    ...adminSubscriptions[0]!,
    ...subscriptionSeeds[0]!,
    account_id: firstAcc.account_id,
    v10_client_id: firstAcc.v10_client_id,
    max_keys: 10,
    created_at: nowSec() - 90 * DAY,
  }
  adminSubscriptions[1] = {
    ...adminSubscriptions[1]!,
    ...subscriptionSeeds[1]!,
    account_id: firstAcc.account_id,
    v10_client_id: firstAcc.v10_client_id,
    max_keys: 5,
    created_at: nowSec() - 60 * DAY,
  }
  adminSubscriptions[2] = {
    ...adminSubscriptions[2]!,
    ...subscriptionSeeds[2]!,
    account_id: firstAcc.account_id,
    v10_client_id: firstAcc.v10_client_id,
    max_keys: 50,
    created_at: nowSec() - 30 * DAY,
  }

  /* ---- ledger: 60 days of consumption + a few credits ----------- */
  const ledger: LedgerEntry[] = []
  let balances = new Map<number, number>()
  let ledgerSeq = 1
  const ledgerAccs = accounts.slice(0, 8)
  ledgerAccs.forEach((acc) => balances.set(acc.account_id, 0))

  const creditSeeds: { offset: number; type: LedgerType; remark: string; refType: string; refId: string; amount: number }[] = [
    { offset: 45, type: 'v10_order', remark: 'V10 订单 续费专业版 99 元', refType: 'v10_order', refId: 'ORD20250918120', amount: 99_000_000 },
    { offset: 30, type: 'recharge', remark: '在线充值 100 元', refType: 'v10_order', refId: 'ORD20251003187', amount: 100_000_000 },
    { offset: 21, type: 'redeem_code', remark: '卡密兑换 50 元', refType: 'redeem_code', refId: 'RC-8842-XKQ1', amount: 50_000_000 },
    { offset: 14, type: 'admin_adjust', remark: '补偿 5 元（工单 #2231 超时扣费）', refType: 'admin_adjust', refId: 'ADJ-2231', amount: 5_000_000 },
    { offset: 9, type: 'refund', remark: '退款 20 元（订单取消）', refType: 'refund', refId: 'REF-9916', amount: -20_000_000 },
    { offset: 3, type: 'admin_adjust', remark: '调账 30 元（销售代充）', refType: 'admin_adjust', refId: 'ADJ-2288', amount: 30_000_000 },
  ]

  for (let d = 60; d >= 0; d--) {
    const acc = ledgerAccs[d % ledgerAccs.length]!
    const spend = between(120_000, 9_800_000)
    const cur = (balances.get(acc.account_id) ?? 0) - spend
    balances.set(acc.account_id, cur)
    ledger.push({
      id: ledgerSeq++,
      account_id: acc.account_id,
      type: 'consume',
      amount_micros: -spend,
      balance_after: cur,
      ref_type: 'usage',
      ref_id: `usage-${dayKey(-d)}`,
      remark: `模型调用消费 ${dayKey(-d)}`,
      created_at: todayStart() - d * DAY + 10 * 3600,
    })
    creditSeeds
      .filter((c) => c.offset === d)
      .forEach((c) => {
        const nb = (balances.get(acc.account_id) ?? 0) + c.amount
        balances.set(acc.account_id, nb)
        ledger.push({
          id: ledgerSeq++,
          account_id: acc.account_id,
          type: c.type,
          amount_micros: c.amount,
          balance_after: nb,
          ref_type: c.refType,
          ref_id: c.refId,
          remark: c.remark,
          created_at: todayStart() - d * DAY + 11 * 3600,
        })
      })
  }
  ledger.sort((a, b) => b.created_at - a.created_at)

  /* ---- channels ------------------------------------------------- */
  const channels: Channel[] = [
    {
      channel_id: 1,
      name: 'deepseek 官方',
      provider: 'openai',
      base_url: 'https://api.deepseek.com',
      models: ['deepseek-chat', 'deepseek-reasoner'],
      model_map: {},
      weight: 100,
      priority: 10,
      max_concurrency: 200,
      timeout_ms: 300_000,
      status: 1,
      key_pool: [
        { index: 0, masked: '****8f2c', weight: 1, status: 'active' },
        { index: 1, masked: '****41ab', weight: 1, status: 'active' },
      ],
      health: { open: false, error_rate: 0.004, samples: 1840 },
    },
    {
      channel_id: 2,
      name: 'OpenAI 中转（主）',
      provider: 'openai',
      base_url: 'https://api.openai-sb.com',
      models: ['gpt-4o-mini', 'gpt-4o'],
      model_map: { 'gpt-4o-mini': 'gpt-4o-mini-2024-07-18' },
      weight: 80,
      priority: 10,
      max_concurrency: 120,
      timeout_ms: 300_000,
      status: 1,
      key_pool: [{ index: 0, masked: '****c0de', weight: 3, status: 'active' }],
      health: { open: false, error_rate: 0.021, samples: 960 },
    },
    {
      channel_id: 3,
      name: '阿里云百炼',
      provider: 'openai',
      base_url: 'https://dashscope.aliyuncs.com/compatible-mode',
      models: ['qwen-plus', 'qwen-max'],
      model_map: {},
      weight: 60,
      priority: 5,
      max_concurrency: 80,
      timeout_ms: 180_000,
      status: 1,
      key_pool: [{ index: 0, masked: '****77e1', weight: 1, status: 'active' }],
      health: { open: false, error_rate: 0.008, samples: 640 },
    },
    {
      channel_id: 4,
      name: '智谱 GLM',
      provider: 'openai',
      base_url: 'https://open.bigmodel.cn/api/paas/v4',
      models: ['glm-4-flash'],
      model_map: {},
      weight: 40,
      priority: 0,
      max_concurrency: 40,
      timeout_ms: 120_000,
      status: 1,
      key_pool: [{ index: 0, masked: '****b3d9', weight: 1, status: 'active' }],
      health: { open: true, error_rate: 0.184, samples: 210 },
    },
    {
      channel_id: 5,
      name: 'Moonshot（备用）',
      provider: 'openai',
      base_url: 'https://api.moonshot.cn',
      models: ['moonshot-v1-8k'],
      model_map: {},
      weight: 20,
      priority: -5,
      max_concurrency: 20,
      timeout_ms: 90_000,
      status: 0,
      key_pool: [{ index: 0, masked: '****a512', weight: 1, status: 'disabled' }],
      health: { open: false, error_rate: 0, samples: 0 },
    },
  ]

  /* ---- admin model pricing rows -------------------------------- */
  const models: AdminModel[] = MODEL_CATALOG.map((m) => ({
    model_name: m.model_name,
    display_name: m.display_name,
    group_name: m.group_name,
    context_len: m.context_len,
    model_ratio: m.model_ratio,
    completion_ratio: m.completion_ratio,
    cached_ratio: m.cached_ratio,
    input_micros: m.input_micros,
    output_micros: m.output_micros,
    capabilities: [...m.capabilities],
    status: 1,
  }))

  /* ---- flags --------------------------------------------------- */
  const flags: FlagValue[] = [
    { key: 'gateway_enabled', value: true, scope: '', description: '总开关。关闭后 /v1/* 全部返回 503 feature_disabled，健康检查不受影响。', updated_at: nowSec() - 3 * DAY },
    { key: 'billing_enforced', value: true, scope: '', description: '关闭后照常计量与落库，但不扣任何额度（记账 billing_mode=PaidByFree）。', updated_at: nowSec() - 12 * DAY },
    { key: 'module_autocreate_enabled', value: true, scope: '', description: 'V10 下单后自动创建账号与订阅，无需人工介入。', updated_at: nowSec() - 20 * DAY },
    { key: 'recharge_enabled', value: false, scope: '', description: '是否允许用户中心跳转 V10 充值页。收款始终在 V10 侧完成。', updated_at: nowSec() - 6 * DAY },
    { key: 'billing_enforced', value: false, scope: '75', description: '账户 75 为测试租户，计量但不扣费。', updated_at: nowSec() - 2 * DAY },
    { key: 'gateway_enabled', value: false, scope: '82', description: '账户 82 因欠费停机。', updated_at: nowSec() - 1 * DAY },
  ]

  /* ---- audit --------------------------------------------------- */
  const actions = [
    '调整账户余额',
    '创建渠道',
    '更新模型倍率',
    '封禁 Key',
    '暂停订阅',
    '轮换站点密钥',
    '切换功能开关',
    '删除套餐',
    '重置订阅额度',
    '创建套餐',
  ]
  const actors = ['admin', 'ops@example.com', 'finance@example.com']
  const audit: AuditEntry[] = Array.from({ length: 48 }, (_, i) => {
    const action = pick(actions)
    const acc = pick(accounts)
    return {
      id: 900 - i,
      actor: pick(actors),
      action,
      target: action.includes('账户') ? `account#${acc.account_id}` : action.includes('渠道') ? `channel#${between(1, 5)}` : `key#${between(300, 320)}`,
      detail: JSON.stringify({ before: { status: 1 }, after: { status: 0 }, note: '手动操作' }).slice(0, 120),
      ip: `10.0.${between(0, 12)}.${between(2, 240)}`,
      created_at: nowSec() - i * 4200 - between(0, 1800),
    }
  })

  /* ---- sites --------------------------------------------------- */
  const sites: (Site & { secret_full: string })[] = [
    {
      app_id: 'app_7f3c9a12',
      name: 'idcsmart 主站',
      base_url: 'https://v10.example.com',
      status: 1,
      secret_tail: 'a91c',
      prev_secret_expires_at: null,
      created_at: nowSec() - 120 * DAY,
      secret_full: 'whsec_7f3c9a12b8e04d6fa91c',
    },
    {
      app_id: 'app_2b8d44e7',
      name: 'idcsmart 香港站',
      base_url: 'https://hk.example.com',
      status: 1,
      secret_tail: '30f7',
      prev_secret_expires_at: nowSec() + 5 * DAY,
      created_at: nowSec() - 64 * DAY,
      secret_full: 'whsec_2b8d44e7c1a94f0d30f7',
    },
    {
      app_id: 'app_5e01cc88',
      name: '测试站（已停用）',
      base_url: 'https://test.example.com',
      status: 0,
      secret_tail: '9b02',
      prev_secret_expires_at: null,
      created_at: nowSec() - 210 * DAY,
      secret_full: 'whsec_5e01cc88dff34a2c9b02',
    },
  ]

  return {
    balanceMicros: firstAcc.balance_micros,
    accounts,
    keys,
    adminSubscriptions,
    ledger,
    channels,
    models,
    plans: PLAN_SEED.map((p) => ({ ...p })) as Plan[],
    flags,
    audit,
    sites,
    seq: 10_000,
  }
}

const store: MockStore = buildStore()

const nextId = () => ++store.seq

/* ================================================================== */
/* Derived series                                                      */
/* ================================================================== */

function buildUserTrend(): TrendPoint[] {
  const out: TrendPoint[] = []
  for (let d = 29; d >= 0; d--) {
    const weekday = new Date((todayStart() - d * DAY) * 1000).getDay()
    const weekendFactor = weekday === 0 || weekday === 6 ? 0.55 : 1
    const wave = 0.75 + 0.5 * Math.abs(Math.sin(d * 0.7))
    const requests = Math.round(between(180, 460) * weekendFactor * wave)
    const prompt = requests * between(420, 900)
    const completion = requests * between(120, 340)
    out.push({
      day: dayKey(-d),
      requests,
      prompt_tokens: prompt,
      completion_tokens: completion,
      cost_micros: Math.round((prompt + completion * 2) * 0.62),
    })
  }
  return out
}

const USER_TREND = buildUserTrend()

function buildAdminTrend(): AdminTrendPoint[] {
  return USER_TREND.map((p, i) => ({
    day: p.day,
    requests: Math.round(p.requests * 6.2 + between(0, 400)),
    cost_micros: Math.round(p.cost_micros * 6.4 + between(0, 2_000_000) + i * 12_000),
  }))
}

const ADMIN_TREND = buildAdminTrend()

function buildUsageLogs(): UsageLog[] {
  const out: UsageLog[] = []
  const models = ALL_MODEL_NAMES
  for (let d = 0; d < 30; d++) {
    const perDay = d === 0 ? 26 : between(34, 62)
    for (let i = 0; i < perDay; i++) {
      const model = pick(models)
      const key = pick(store.keys)
      const prompt = between(80, 4200)
      const completion = between(20, 1600)
      const stream = rnd() > 0.35
      const statusCode = rnd() > 0.972 ? pick([401, 429, 500, 503]) : 200
      const ttft = stream ? between(120, 1400) : between(180, 900)
      const latency = ttft + between(200, 4200)
      const modelMeta = MODEL_CATALOG.find((m) => m.model_name === model)!
      const cost =
        statusCode === 200
          ? Math.round((prompt * modelMeta.input_micros + completion * modelMeta.output_micros) / 1_000_000)
          : 0
      out.push({
        request_id: `req_${(0x1c8 + d * 1000 + i).toString(16).toUpperCase()}${Math.floor(rnd() * 1e6).toString(16).toUpperCase()}`,
        model,
        prompt_tokens: statusCode === 200 ? prompt : 0,
        completion_tokens: statusCode === 200 ? completion : 0,
        cost_micros: cost,
        billing_mode: statusCode === 200 ? pick(['subscription', 'balance', 'hybrid'] as const) : 'free',
        stream,
        estimated: rnd() > 0.9,
        status_code: statusCode,
        latency_ms: latency,
        ttft_ms: statusCode === 200 ? ttft : 0,
        api_key_id: key.key_id,
        v10_host_id: key.v10_host_id,
        created_at: todayStart() - d * DAY + between(3600 * 9, 3600 * 22),
      })
    }
  }
  return out.sort((a, b) => b.created_at - a.created_at)
}

const USAGE_LOGS = buildUsageLogs()

/* ================================================================== */
/* Helpers                                                            */
/* ================================================================== */

function paginate<T>(items: T[], page = 1, pageSize = 20): Paged<T> {
  const p = Math.max(1, page)
  const size = Math.min(200, Math.max(1, pageSize))
  const start = (p - 1) * size
  return { total: items.length, page: p, page_size: size, items: items.slice(start, start + size) }
}

function sumTotals(items: { requests: number; prompt_tokens: number; completion_tokens: number; cost_micros: number }[]): UsageTotals {
  return items.reduce<UsageTotals>(
    (acc, x) => ({
      requests: acc.requests + x.requests,
      prompt_tokens: acc.prompt_tokens + x.prompt_tokens,
      completion_tokens: acc.completion_tokens + x.completion_tokens,
      cost_micros: acc.cost_micros + x.cost_micros,
    }),
    { requests: 0, prompt_tokens: 0, completion_tokens: 0, cost_micros: 0 },
  )
}

function randHex(n: number): string {
  const chars = '0123456789abcdef'
  let s = ''
  for (let i = 0; i < n; i++) s += chars[Math.floor(rnd() * 16)]
  return s
}

/** Models available to the current account: everything in its plan groups. */
export function mockAvailableModels(): ConsoleModel[] {
  return MODEL_CATALOG.filter((m) => m.group_name !== 'premium').map((m) => ({
    model_name: m.model_name,
    display_name: m.display_name,
    group_name: m.group_name,
    context_len: m.context_len,
    capabilities: [...m.capabilities],
    ratio: {
      model_ratio: m.model_ratio,
      completion_ratio: m.completion_ratio,
      cached_ratio: m.cached_ratio,
    },
    price_hint: {
      input_micros_per_1m: m.input_micros,
      output_micros_per_1m: m.output_micros,
    },
  }))
}

/* ================================================================== */
/* Console endpoints                                                   */
/* ================================================================== */

export const mockConsole = {
  async overview(): Promise<Overview> {
    const todayReq = USER_TREND[USER_TREND.length - 1]!
    const month = USER_TREND.slice(-30)
    const acc = store.accounts[0]!
    return lag<Overview>({
      account: { v10_client_id: acc.v10_client_id, email: acc.email, status: acc.status },
      balance_micros: store.balanceMicros,
      today: sumTotals([{ requests: todayReq.requests, prompt_tokens: todayReq.prompt_tokens, completion_tokens: todayReq.completion_tokens, cost_micros: todayReq.cost_micros }]),
      month: sumTotals(month),
      subscriptions: store.adminSubscriptions
        .filter((s) => s.account_id === acc.account_id)
        .map<SubscriptionSummary>((s) => ({
          subscription_id: s.subscription_id,
          v10_host_id: s.v10_host_id,
          plan_code: s.plan_code,
          plan_name: s.plan_name,
          status: s.status,
          billing_mode: s.billing_mode,
          period_start: s.period_start,
          period_end: s.period_end,
          quota_total: s.quota_total,
          quota_used: s.quota_used,
          quota_remaining: s.quota_remaining,
        })),
      trend: USER_TREND,
      recharge_url: 'https://v10.example.com/plugin/idcsmart_llm_gateway/recharge.html',
    })
  },

  async listKeys(): Promise<ApiKey[]> {
    const items = [...store.keys].sort((a, b) => b.key_id - a.key_id)
    return lag(items)
  },

  async createKey(payload: CreateKeyRequest): Promise<CreateKeyResponse> {
    await sleep(320)
    const plaintext = `sk-${randHex(8)}${randHex(32)}`
    const key: ApiKey = {
      key_id: nextId(),
      key_prefix: plaintext.slice(0, 11),
      name: payload.name,
      status: 'active',
      subscription_id: payload.subscription_id,
      v10_host_id: store.adminSubscriptions.find((s) => s.subscription_id === payload.subscription_id)?.v10_host_id ?? 555,
      models: [...payload.models],
      ip_whitelist: [...payload.ip_whitelist],
      expires_at: payload.expires_at ?? null,
      last_used_at: null,
      created_at: nowSec(),
      usage_micros_30d: 0,
      requests_30d: 0,
    }
    store.keys.unshift(key)
    return {
      key_id: key.key_id,
      key: plaintext,
      key_prefix: key.key_prefix,
      name: key.name,
      status: key.status,
      created_at: key.created_at,
    }
  },

  async updateKey(id: number, patch: Partial<ApiKey>): Promise<ApiKey> {
    await sleep(200)
    const key = store.keys.find((k) => k.key_id === id)
    if (!key) throw new Error('Key 不存在')
    Object.assign(key, patch)
    return { ...key }
  },

  async revokeKey(id: number): Promise<{ key_id: number; status: ApiKey['status'] }> {
    await sleep(220)
    const key = store.keys.find((k) => k.key_id === id)
    if (!key) throw new Error('Key 不存在')
    key.status = 'revoked'
    return { key_id: id, status: 'revoked' }
  },

  async models(): Promise<{ total: number; items: ConsoleModel[] }> {
    const items = mockAvailableModels()
    return lag({ total: items.length, items })
  },

  async usageSummary(from: number, to: number, groupBy: 'day' | 'model' | 'key' | 'host'): Promise<UsageSummary> {
    const logs = USAGE_LOGS.filter((l) => l.created_at >= from && l.created_at <= to && l.status_code === 200)
    const buckets = new Map<string, UsageTotals>()
    const bump = (k: string, l: UsageLog) => {
      const cur = buckets.get(k) ?? { requests: 0, prompt_tokens: 0, completion_tokens: 0, cost_micros: 0 }
      cur.requests += 1
      cur.prompt_tokens += l.prompt_tokens
      cur.completion_tokens += l.completion_tokens
      cur.cost_micros += l.cost_micros
      buckets.set(k, cur)
    }
    for (const l of logs) {
      if (groupBy === 'day') {
        const d = new Date(l.created_at * 1000)
        const p = (n: number) => String(n).padStart(2, '0')
        bump(`${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`, l)
      } else if (groupBy === 'model') {
        bump(l.model, l)
      } else if (groupBy === 'key') {
        bump(`key#${l.api_key_id}`, l)
      } else {
        bump(`host#${l.v10_host_id}`, l)
      }
    }
    const list = [...buckets.entries()]
      .map(([key, v]) => ({ key, ...v }))
      .sort((a, b) => (groupBy === 'day' ? a.key.localeCompare(b.key) : b.cost_micros - a.cost_micros))
    return lag({
      totals: sumTotals(list),
      buckets: list,
    })
  },

  async usageLogs(q: UsageLogQuery): Promise<Paged<UsageLog>> {
    let rows = USAGE_LOGS
    if (q.from) rows = rows.filter((r) => r.created_at >= q.from!)
    if (q.to) rows = rows.filter((r) => r.created_at <= q.to!)
    if (q.model) rows = rows.filter((r) => r.model === q.model)
    if (q.api_key_id) rows = rows.filter((r) => r.api_key_id === q.api_key_id)
    return lag(paginate(rows, q.page, q.page_size), 260)
  },

  async docs(): Promise<DocsPayload> {
    const base = 'https://api.example.com'
    return lag({
      base_url: base,
      examples: {
        curl: `curl ${base}/v1/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer $CZAPI_KEY" \\
  -d '{
    "model": "deepseek-chat",
    "messages": [{"role": "user", "content": "用一句话解释什么是网关"}],
    "stream": true
  }'`,
        python: `from openai import OpenAI

client = OpenAI(
    api_key="sk-你的Key",
    base_url="${base}/v1",
)

resp = client.chat.completions.create(
    model="deepseek-chat",
    messages=[{"role": "user", "content": "用一句话解释什么是网关"}],
    stream=True,
)
for chunk in resp:
    delta = chunk.choices[0].delta
    if delta.content:
        print(delta.content, end="", flush=True)`,
        node: `import OpenAI from 'openai'

const client = new OpenAI({
  apiKey: process.env.CZAPI_KEY,
  baseURL: '${base}/v1',
})

const stream = await client.chat.completions.create({
  model: 'deepseek-chat',
  messages: [{ role: 'user', content: '用一句话解释什么是网关' }],
  stream: true,
})

for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content ?? '')
}`,
        langchain: `from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    model="deepseek-chat",
    api_key="sk-你的Key",
    base_url="${base}/v1",
    streaming=True,
    temperature=0.3,
)

for chunk in llm.stream("用一句话解释什么是网关"):
    print(chunk.content, end="", flush=True)`,
      },
    })
  },
}

/* ================================================================== */
/* Admin endpoints                                                     */
/* ================================================================== */

export const mockAdmin = {
  async login(username: string, password: string): Promise<LoginResponse> {
    await sleep(420)
    if (!username || !password) throw new Error('请输入用户名和密码')
    if (password.length < 4) throw new Error('用户名或密码错误（mock：密码至少 4 位）')
    return {
      token: `mock.jwt.${randHex(24)}`,
      expires_at: nowSec() + 12 * 3600,
      admin: { id: 1, username, role: 'super_admin' },
    }
  },

  async dashboard(): Promise<Dashboard> {
    const last = ADMIN_TREND[ADMIN_TREND.length - 1]!
    const activeKeys = store.keys.filter((k) => k.status === 'active').length
    const activeSubs = store.adminSubscriptions.filter((s) => s.status === 'active').length
    return lag<Dashboard>({
      requests_today: last.requests,
      cost_today_micros: last.cost_micros,
      cost_month_micros: ADMIN_TREND.reduce((a, b) => a + b.cost_micros, 0),
      error_rate_5m: 0.0112,
      qps_5m: 3.42,
      ttft_p95_ms_5m: 812,
      active_keys: activeKeys,
      active_subscriptions: activeSubs,
      accounts: store.accounts.length,
      channels: store.channels.map((c) => ({
        channel_id: c.channel_id,
        name: c.name,
        status: c.status,
        open: c.health.open,
        error_rate: c.health.error_rate,
      })),
      top_models: store.models
        .filter((m) => m.status === 1)
        .slice(0, 5)
        .map((m, i) => ({
          model: m.model_name,
          requests: Math.round(last.requests / (i + 2.4)),
          cost_micros: Math.round(last.cost_micros / (i + 2.8)),
        })),
      top_accounts: store.accounts.slice(0, 5).map((a, i) => ({
        account_id: a.account_id,
        v10_client_id: a.v10_client_id,
        email: a.email,
        cost_micros: Math.round(last.cost_micros / (i + 3.1)),
      })),
      trend: ADMIN_TREND,
      reconcile: {
        last_run: todayStart() - 9 * 3600,
        diff_micros: 0,
        ok: true,
      },
    })
  },

  /* ---- channels ------------------------------------------------- */
  async listChannels(): Promise<Channel[]> {
    return lag(store.channels.map((c) => ({ ...c })))
  },

  async getChannel(id: number): Promise<Channel> {
    const c = store.channels.find((x) => x.channel_id === id)
    if (!c) throw new Error('渠道不存在')
    return lag({ ...c })
  },

  async createChannel(payload: Omit<ChannelWrite, 'key_pool'> & { key_pool: ChannelKeySlotWrite[] }): Promise<Channel> {
    await sleep(300)
    const channel: Channel = {
      ...payload,
      channel_id: nextId(),
      key_pool: payload.key_pool.map((k, i) => ({
        index: i,
        masked: k.secret ? `****${k.secret.slice(-4)}` : '****pending',
        weight: k.weight,
        status: k.status,
      })),
      health: { open: false, error_rate: 0, samples: 0 },
    }
    store.channels.push(channel)
    return { ...channel }
  },

  async updateChannel(id: number, payload: Partial<Omit<ChannelWrite, 'key_pool'>> & { key_pool?: ChannelKeySlotWrite[] }): Promise<Channel> {
    await sleep(260)
    const c = store.channels.find((x) => x.channel_id === id)
    if (!c) throw new Error('渠道不存在')
    const { key_pool, ...rest } = payload
    Object.assign(c, rest)
    if (key_pool) {
      c.key_pool = key_pool.map((k, i) => ({
        index: i,
        // A slot without a new secret keeps its previous masked tail.
        masked: k.secret ? `****${k.secret.slice(-4)}` : (c.key_pool[i]?.masked ?? '****keep'),
        weight: k.weight,
        status: k.status,
      }))
    }
    return { ...c }
  },

  async deleteChannel(id: number): Promise<{ channel_id: number }> {
    await sleep(200)
    const i = store.channels.findIndex((c) => c.channel_id === id)
    if (i < 0) throw new Error('渠道不存在')
    store.channels.splice(i, 1)
    return { channel_id: id }
  },

  async testChannel(id: number): Promise<ChannelTestResult> {
    await sleep(900)
    const c = store.channels.find((x) => x.channel_id === id)
    if (!c) throw new Error('渠道不存在')
    if (c.status === 0) return { ok: false, message: '渠道已停用，跳过探测', latency_ms: 0 }
    if (c.health.open) return { ok: false, message: '熔断器处于 open 状态，上游连续失败', latency_ms: 0 }
    const latency = between(90, 620)
    return { ok: true, message: `GET /v1/models 返回 200，发现 ${c.models.length} 个模型`, latency_ms: latency }
  },

  async syncChannelModels(id: number): Promise<ChannelSyncResult> {
    await sleep(1100)
    const c = store.channels.find((x) => x.channel_id === id)
    if (!c) throw new Error('渠道不存在')
    const upstream = ALL_MODEL_NAMES.filter((m) => m.includes(c.name.split(' ')[0]!.toLowerCase()) || rnd() > 0.72)
    const added = upstream.filter((m) => !c.models.includes(m))
    c.models = [...new Set([...c.models, ...upstream])]
    return { added, total: c.models.length }
  },

  /* ---- models --------------------------------------------------- */
  async listModels(): Promise<AdminModel[]> {
    return lag(store.models.map((m) => ({ ...m })))
  },

  async upsertModel(payload: AdminModel): Promise<AdminModel> {
    await sleep(240)
    const i = store.models.findIndex((m) => m.model_name === payload.model_name)
    if (i >= 0) store.models[i] = { ...payload }
    else store.models.unshift({ ...payload })
    return { ...payload }
  },

  async deleteModel(name: string): Promise<{ model_name: string }> {
    await sleep(200)
    const i = store.models.findIndex((m) => m.model_name === name)
    if (i < 0) throw new Error('模型不存在')
    store.models.splice(i, 1)
    return { model_name: name }
  },

  /* ---- plans ---------------------------------------------------- */
  async listPlans(): Promise<Plan[]> {
    return lag(store.plans.map((p) => ({ ...p })))
  },

  async upsertPlan(payload: Plan): Promise<Plan> {
    await sleep(240)
    const i = store.plans.findIndex((p) => p.plan_code === payload.plan_code)
    if (i >= 0) store.plans[i] = { ...payload }
    else store.plans.push({ ...payload })
    return { ...payload }
  },

  async deletePlan(code: string): Promise<{ plan_code: string }> {
    await sleep(200)
    const i = store.plans.findIndex((p) => p.plan_code === code)
    if (i < 0) throw new Error('套餐不存在')
    store.plans.splice(i, 1)
    return { plan_code: code }
  },

  /* ---- accounts ------------------------------------------------- */
  async listAccounts(q: string, page = 1, pageSize = 20): Promise<Paged<Account>> {
    const needle = q.trim().toLowerCase()
    const rows = store.accounts.filter(
      (a) =>
        !needle ||
        a.email.toLowerCase().includes(needle) ||
        String(a.v10_client_id).includes(needle) ||
        String(a.account_id).includes(needle),
    )
    return lag(paginate(rows, page, pageSize))
  },

  async getAccount(id: number): Promise<AccountDetail> {
    await sleep(300)
    const acc = store.accounts.find((a) => a.account_id === id)
    if (!acc) throw new Error('账户不存在')
    const subs = store.adminSubscriptions.filter((s) => s.account_id === id)
    const keys = store.keys.filter((_, i) => i % 3 === id % 3).slice(0, 6)
    const trend: TrendPoint[] = Array.from({ length: 30 }, (_, i) => {
      const d = 29 - i
      const requests = between(4, 180)
      const prompt = requests * between(400, 900)
      const completion = requests * between(120, 320)
      return {
        day: dayKey(-d),
        requests,
        prompt_tokens: prompt,
        completion_tokens: completion,
        cost_micros: Math.round((prompt + completion * 2) * 0.58),
      }
    })
    return {
      ...acc,
      subscriptions: subs as AccountDetail['subscriptions'],
      keys,
      usage_30d: {
        requests: trend.reduce((a, b) => a + b.requests, 0),
        prompt_tokens: trend.reduce((a, b) => a + b.prompt_tokens, 0),
        completion_tokens: trend.reduce((a, b) => a + b.completion_tokens, 0),
        cost_micros: trend.reduce((a, b) => a + b.cost_micros, 0),
      },
      trend,
    }
  },

  async adjustAccount(id: number, payload: { amount_micros: number; remark: string; ref_id: string }): Promise<AdjustAccountResponse> {
    await sleep(360)
    const acc = store.accounts.find((a) => a.account_id === id)
    if (!acc) throw new Error('账户不存在')
    if (!payload.remark?.trim()) throw new Error('备注不能为空')
    if (!payload.ref_id?.trim()) throw new Error('ref_id 不能为空（幂等凭证）')
    const dup = store.ledger.some((l) => l.ref_type === 'admin_adjust' && l.ref_id === payload.ref_id)
    if (dup) {
      return { applied: false, account_id: id, balance_micros: acc.balance_micros, ledger_entry_id: 0 }
    }
    acc.balance_micros += payload.amount_micros
    if (acc.account_id === store.accounts[0]!.account_id) store.balanceMicros = acc.balance_micros
    const entry: LedgerEntry = {
      id: nextId(),
      account_id: id,
      type: 'admin_adjust',
      amount_micros: payload.amount_micros,
      balance_after: acc.balance_micros,
      ref_type: 'admin_adjust',
      ref_id: payload.ref_id,
      remark: payload.remark,
      created_at: nowSec(),
    }
    store.ledger.unshift(entry)
    return { applied: true, account_id: id, balance_micros: acc.balance_micros, ledger_entry_id: entry.id }
  },

  async updateAccount(id: number, payload: { status?: number; allow_negative?: boolean }): Promise<Account> {
    await sleep(220)
    const acc = store.accounts.find((a) => a.account_id === id)
    if (!acc) throw new Error('账户不存在')
    Object.assign(acc, payload)
    return { ...acc }
  },

  async searchKeys(q: string, status: string, accountId: number | undefined, page = 1, pageSize = 20): Promise<Paged<AdminKey>> {
    const needle = q.trim().toLowerCase()
    let rows: AdminKey[] = store.keys.map((k, i) => ({
      ...k,
      account_id: store.accounts[i % store.accounts.length]!.account_id,
      email: store.accounts[i % store.accounts.length]!.email,
      v10_client_id: store.accounts[i % store.accounts.length]!.v10_client_id,
    }))
    if (needle) rows = rows.filter((k) => k.key_prefix.toLowerCase().includes(needle) || k.name.toLowerCase().includes(needle) || (k.email ?? '').toLowerCase().includes(needle))
    if (status) rows = rows.filter((k) => k.status === status)
    if (accountId) rows = rows.filter((k) => k.account_id === accountId)
    return lag(paginate(rows, page, pageSize))
  },

  async banKey(id: number, status: 'active' | 'suspended'): Promise<AdminKey> {
    await sleep(220)
    const key = store.keys.find((k) => k.key_id === id)
    if (!key) throw new Error('Key 不存在')
    key.status = status
    const idx = store.keys.findIndex((k) => k.key_id === id)
    return { ...key, account_id: store.accounts[idx % store.accounts.length]!.account_id }
  },

  /* ---- subscriptions -------------------------------------------- */
  async listSubscriptions(status: string, page = 1, pageSize = 20): Promise<Paged<AdminSubscription>> {
    const rows = store.adminSubscriptions.filter((s) => !status || s.status === status)
    return lag(paginate(rows, page, pageSize))
  },

  async getSubscription(id: number): Promise<AdminSubscription> {
    const s = store.adminSubscriptions.find((x) => x.subscription_id === id)
    if (!s) throw new Error('订阅不存在')
    return lag({ ...s })
  },

  async subscriptionAction(id: number, action: 'suspend' | 'resume' | 'reset-quota'): Promise<SubscriptionActionResponse> {
    await sleep(280)
    const s = store.adminSubscriptions.find((x) => x.subscription_id === id)
    if (!s) throw new Error('订阅不存在')
    if (action === 'suspend') s.status = 'suspended'
    if (action === 'resume') s.status = 'active'
    if (action === 'reset-quota') {
      s.quota_used = 0
      s.quota_remaining = s.quota_total
    }
    return {
      subscription_id: s.subscription_id,
      status: s.status,
      quota_used: s.quota_used,
      quota_remaining: s.quota_remaining,
    }
  },

  /* ---- ledger --------------------------------------------------- */
  async listLedger(params: { account_id?: number; type?: string; ref_type?: string; from?: number; to?: number; page?: number; page_size?: number }): Promise<Paged<LedgerEntry>> {
    let rows = store.ledger
    if (params.account_id) rows = rows.filter((l) => l.account_id === params.account_id)
    if (params.type) rows = rows.filter((l) => l.type === params.type)
    if (params.ref_type) rows = rows.filter((l) => l.ref_type === params.ref_type)
    if (params.from) rows = rows.filter((l) => l.created_at >= params.from!)
    if (params.to) rows = rows.filter((l) => l.created_at <= params.to!)
    return lag(paginate(rows, params.page, params.page_size ?? 20))
  },

  /* ---- reconcile ------------------------------------------------ */
  async reconcile(day: string): Promise<ReconcileReport> {
    await sleep(520)
    const isToday = day === dayKey(0)
    const base = between(38_000_000, 52_000_000)
    const drift = isToday ? 0 : rnd() > 0.72 ? between(1_000, 240_000) : 0
    const details = store.accounts.slice(0, 9).map((a, i) => {
      const usage = Math.round(base / (i + 2.2)) + between(0, 20_000)
      const rowDrift = i === 3 && drift > 0 ? drift : 0
      return {
        account_id: a.account_id,
        v10_host_id: 500 + i * 7,
        usage_micros: usage,
        ledger_micros: usage - rowDrift,
        diff_micros: rowDrift,
      }
    })
    const usageTotal = details.reduce((x, r) => x + r.usage_micros, 0)
    return {
      day,
      usage_cost_micros: usageTotal,
      subscription_consumed_micros: usageTotal - Math.round(drift / 2),
      balance_consumed_micros: Math.round(drift / 2),
      ok: drift === 0,
      diff_micros: drift,
      details,
    }
  },

  /* ---- flags ---------------------------------------------------- */
  async listFlags(): Promise<FlagValue[]> {
    return lag(store.flags.map((f) => ({ ...f })))
  },

  async setFlag(key: string, value: boolean, scope: string): Promise<FlagValue> {
    await sleep(220)
    const existing = store.flags.find((f) => f.key === key && f.scope === scope)
    if (existing) {
      existing.value = value
      existing.updated_at = nowSec()
      return { ...existing }
    }
    const created: FlagValue = {
      key,
      value,
      scope,
      description: scope ? `账户 ${scope} 的覆盖值` : '',
      updated_at: nowSec(),
    }
    store.flags.push(created)
    return { ...created }
  },

  /* ---- audit ---------------------------------------------------- */
  async listAudit(page = 1, pageSize = 20): Promise<Paged<AuditEntry>> {
    return lag(paginate(store.audit, page, pageSize))
  },

  /* ---- system --------------------------------------------------- */
  async settings(): Promise<SystemSettings> {
    return lag({
      server: {
        version: '1.0.0',
        build_time: '2026-11-04T18:22:11+08:00',
        go_version: 'go1.27.1',
        uptime_seconds: 386_412,
        env: 'production',
      },
      storage: { driver: 'mysql', dsn: 'czapi:***@tcp(10.0.2.11:3306)/czapi' },
      cache: { driver: 'redis', addr: '10.0.2.12:6379', db: 0 },
      queue: { driver: 'redis', topic_usage: 'cz.usage' },
      limits: {
        key_rpm_default: 60,
        key_concurrency_default: 5,
        account_month_cap_micros: 900_000_000,
        site_day_cap_micros: 4_500_000_000,
      },
      auth: {
        jwt_ttl_seconds: 43200,
        key_cache_ttl_seconds: 300,
        internal_hmac_window_seconds: 300,
      },
      retention: { usage_days: 180, audit_days: 365, ledger_days: 1095 },
      relay: {
        max_retries: 2,
        breaker_window_seconds: 60,
        breaker_error_threshold: 0.5,
        breaker_min_samples: 20,
      },
    })
  },

  /* ---- sites ---------------------------------------------------- */
  async listSites(): Promise<Site[]> {
    return lag(
      store.sites.map(({ secret_full: _secret, ...s }) => ({ ...s })),
    )
  },

  async createSite(payload: SiteCreateRequest): Promise<SiteCreateResponse> {
    await sleep(420)
    const appId = `app_${randHex(8)}`
    const secret = `whsec_${randHex(20)}`
    const site: Site & { secret_full: string } = {
      app_id: appId,
      name: payload.name,
      base_url: payload.base_url,
      status: 1,
      secret_tail: secret.slice(-4),
      prev_secret_expires_at: null,
      created_at: nowSec(),
      secret_full: secret,
    }
    store.sites.push(site)
    return {
      app_id: appId,
      secret,
      name: payload.name,
      base_url: payload.base_url,
      created_at: site.created_at,
    }
  },

  async rotateSiteSecret(appId: string): Promise<SiteRotateResponse> {
    await sleep(380)
    const site = store.sites.find((s) => s.app_id === appId)
    if (!site) throw new Error('站点不存在')
    const secret = `whsec_${randHex(20)}`
    site.secret_full = secret
    site.secret_tail = secret.slice(-4)
    site.prev_secret_expires_at = nowSec() + 7 * DAY
    return { app_id: appId, secret, prev_expires_at: nowSec() + 7 * DAY }
  },
}
