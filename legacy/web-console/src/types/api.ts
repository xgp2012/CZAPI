/**
 * TypeScript mirror of docs/console-api-v1.md (frozen contract).
 *
 * Rules baked into these types:
 *  - Every field name is snake_case and matches the document exactly.
 *  - All timestamps are unix seconds (int64) — format at the edge, never here.
 *  - Every money field ends with `_micros`; 1 元 = 1_000_000 micros.
 */

/* ------------------------------------------------------------------ */
/* Envelope & pagination                                              */
/* ------------------------------------------------------------------ */

export interface Envelope<T> {
  code: number
  msg: string
  data: T
}

export interface Paged<T> {
  total: number
  page: number
  page_size: number
  items: T[]
}

/** `GET /console/v1/models` is a paged envelope without page metadata. */
export interface Totalled<T> {
  total: number
  items: T[]
}

export interface PageQuery {
  page?: number
  page_size?: number
}

/* ------------------------------------------------------------------ */
/* §1.2 Overview                                                      */
/* ------------------------------------------------------------------ */

export interface AccountBrief {
  v10_client_id: number
  email: string
  /** 1 = active, 0 = disabled. */
  status: number
}

export interface UsageTotals {
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cost_micros: number
}

export interface SubscriptionSummary {
  subscription_id: number
  v10_host_id: number
  plan_code: string
  plan_name: string
  status: SubscriptionStatus
  billing_mode: BillingMode
  period_start: number
  period_end: number
  quota_total: number
  quota_used: number
  quota_remaining: number
}

export interface TrendPoint {
  /** YYYY-MM-DD */
  day: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cost_micros: number
}

export interface Overview {
  account: AccountBrief
  balance_micros: number
  today: UsageTotals
  month: UsageTotals
  subscriptions: SubscriptionSummary[]
  trend: TrendPoint[]
  recharge_url: string
}

/* ------------------------------------------------------------------ */
/* §1.3 Key management                                                */
/* ------------------------------------------------------------------ */

export type KeyStatus = 'active' | 'suspended' | 'revoked'

export interface ApiKey {
  key_id: number
  key_prefix: string
  name: string
  status: KeyStatus
  subscription_id: number
  v10_host_id: number
  models: string[]
  ip_whitelist: string[]
  /** null = never expires. */
  expires_at: number | null
  last_used_at: number | null
  created_at: number
  usage_micros_30d: number
  requests_30d: number
}

export interface CreateKeyRequest {
  name: string
  subscription_id: number
  models: string[]
  ip_whitelist: string[]
  expires_at?: number | null
}

/** Responded exactly once; `key` is the only time the plaintext exists. */
export interface CreateKeyResponse {
  key_id: number
  /** Plaintext key, `null` on an idempotent replay. */
  key: string | null
  key_prefix: string
  name: string
  status: KeyStatus
  created_at: number
}

export interface UpdateKeyRequest {
  name?: string
  status?: 'active' | 'suspended'
  models?: string[]
  ip_whitelist?: string[]
  expires_at?: number | null
}

export interface RevokeKeyResponse {
  key_id: number
  status: KeyStatus
}

/* ------------------------------------------------------------------ */
/* §1.4 Models                                                        */
/* ------------------------------------------------------------------ */

export interface ModelRatio {
  model_ratio: number
  completion_ratio: number
  cached_ratio: number
}

export interface PriceHint {
  input_micros_per_1m: number
  output_micros_per_1m: number
}

export interface ConsoleModel {
  model_name: string
  display_name: string
  group_name: string
  context_len: number
  capabilities: string[]
  ratio: ModelRatio
  price_hint: PriceHint
}

/* ------------------------------------------------------------------ */
/* §1.5 Usage                                                         */
/* ------------------------------------------------------------------ */

export type UsageGroupBy = 'day' | 'model' | 'key' | 'host'

export interface UsageBucket extends UsageTotals {
  key: string
}

export interface UsageSummary {
  totals: UsageTotals
  buckets: UsageBucket[]
}

export interface UsageLog {
  request_id: string
  model: string
  prompt_tokens: number
  completion_tokens: number
  cost_micros: number
  billing_mode: BillingMode
  stream: boolean
  estimated: boolean
  status_code: number
  latency_ms: number
  ttft_ms: number
  api_key_id: number
  v10_host_id: number
  created_at: number
}

export interface UsageLogQuery extends PageQuery {
  from?: number
  to?: number
  api_key_id?: number
  model?: string
}

/* ------------------------------------------------------------------ */
/* §1.6 Docs                                                          */
/* ------------------------------------------------------------------ */

export interface DocsPayload {
  base_url: string
  examples: {
    curl: string
    python: string
    node: string
    langchain: string
  }
}

/* ------------------------------------------------------------------ */
/* §2.1 Admin auth                                                    */
/* ------------------------------------------------------------------ */

export interface AdminUser {
  id: number
  username: string
  role: string
}

export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  token: string
  expires_at: number
  admin: AdminUser
}

/* ------------------------------------------------------------------ */
/* §2.2 Admin dashboard                                               */
/* ------------------------------------------------------------------ */

export interface ChannelHealthRow {
  channel_id: number
  name: string
  status: number
  /** Breaker open (tripped). */
  open: boolean
  error_rate: number
}

export interface TopModel {
  model: string
  requests: number
  cost_micros: number
}

export interface TopAccount {
  account_id: number
  v10_client_id: number
  email: string
  cost_micros: number
}

export interface AdminTrendPoint {
  day: string
  requests: number
  cost_micros: number
}

export interface ReconcileStatus {
  last_run: number | null
  diff_micros: number
  ok: boolean
}

export interface Dashboard {
  requests_today: number
  cost_today_micros: number
  cost_month_micros: number
  error_rate_5m: number
  qps_5m: number
  ttft_p95_ms_5m: number
  active_keys: number
  active_subscriptions: number
  accounts: number
  channels: ChannelHealthRow[]
  top_models: TopModel[]
  top_accounts: TopAccount[]
  trend: AdminTrendPoint[]
  reconcile: ReconcileStatus
}

/* ------------------------------------------------------------------ */
/* §2.3 Channels                                                      */
/* ------------------------------------------------------------------ */

export interface ChannelKeySlotRead {
  index: number
  /** Only the last 4 characters, e.g. `****abcd`. */
  masked: string
  weight: number
  status: string
}

/** Write-only shape: the plaintext secret is never read back. */
export interface ChannelKeySlotWrite {
  secret?: string
  weight: number
  status: string
}

export interface ChannelHealth {
  open: boolean
  error_rate: number
  samples: number
}

export interface Channel {
  channel_id: number
  name: string
  provider: string
  base_url: string
  models: string[]
  model_map: Record<string, string>
  weight: number
  priority: number
  max_concurrency: number
  timeout_ms: number
  status: number
  key_pool: ChannelKeySlotRead[]
  health: ChannelHealth
}

export interface ChannelWrite {
  name: string
  provider: string
  base_url: string
  models: string[]
  model_map: Record<string, string>
  weight: number
  priority: number
  max_concurrency: number
  timeout_ms: number
  status: number
  key_pool: ChannelKeySlotWrite[]
}

export interface ChannelTestResult {
  ok: boolean
  message: string
  latency_ms: number
}

export interface ChannelSyncResult {
  added: string[]
  total: number
}

/* ------------------------------------------------------------------ */
/* §2.4 Admin models                                                  */
/* ------------------------------------------------------------------ */

export interface AdminModel {
  model_name: string
  display_name: string
  group_name: string
  context_len: number
  model_ratio: number
  completion_ratio: number
  cached_ratio: number
  input_micros: number
  output_micros: number
  capabilities: string[]
  status: number
}

/* ------------------------------------------------------------------ */
/* §2.5 Plans                                                         */
/* ------------------------------------------------------------------ */

export type PlanPeriod = 'month' | 'quarter' | 'year' | 'once'

export interface Plan {
  plan_code: string
  name: string
  quota_tokens: number
  period: PlanPeriod
  rpm_limit: number
  tpm_limit: number
  concurrency: number
  model_group: string
  max_keys: number
  price_cents: number
  group_ratio: number
  status: number
}

/* ------------------------------------------------------------------ */
/* §2.6 Accounts & keys                                               */
/* ------------------------------------------------------------------ */

export type SubscriptionStatus = 'active' | 'suspended' | 'expired' | 'pending'
export type BillingMode = 'subscription' | 'balance' | 'hybrid' | 'free'

export interface Account {
  account_id: number
  v10_client_id: number
  email: string
  status: number
  allow_negative: boolean
  balance_micros: number
  created_at: number
}

export type AccountSubscription = SubscriptionSummary & { account_id?: number }

export interface AccountDetail extends Account {
  subscriptions: AccountSubscription[]
  keys: ApiKey[]
  usage_30d: {
    requests: number
    prompt_tokens: number
    completion_tokens: number
    cost_micros: number
  }
  /** Optional per-day series for the detail drawer. */
  trend?: TrendPoint[]
}

export interface AdjustAccountRequest {
  amount_micros: number
  remark: string
  ref_id: string
}

export interface AdjustAccountResponse {
  applied: boolean
  account_id: number
  balance_micros: number
  ledger_entry_id: number
}

export interface UpdateAccountRequest {
  status?: number
  allow_negative?: boolean
}

export interface AdminKey extends ApiKey {
  account_id?: number
  email?: string
  v10_client_id?: number
}

export interface AdminKeyQuery extends PageQuery {
  q?: string
  status?: KeyStatus | ''
  account_id?: number
}

/* ------------------------------------------------------------------ */
/* §2.7 Subscriptions                                                 */
/* ------------------------------------------------------------------ */

export interface AdminSubscription {
  subscription_id: number
  account_id: number
  v10_client_id: number
  v10_host_id: number
  plan_code: string
  plan_name: string
  status: SubscriptionStatus
  billing_mode: BillingMode
  period_start: number
  period_end: number
  quota_total: number
  quota_used: number
  quota_remaining: number
  max_keys: number
  created_at: number
}

export interface SubscriptionActionResponse {
  subscription_id: number
  status: SubscriptionStatus
  quota_used?: number
  quota_remaining?: number
}

/* ------------------------------------------------------------------ */
/* §2.8 Ledger                                                        */
/* ------------------------------------------------------------------ */

export type LedgerType =
  | 'recharge'
  | 'consume'
  | 'refund'
  | 'admin_adjust'
  | 'redeem_code'
  | 'v10_order'
  | 'expire'

export interface LedgerEntry {
  id: number
  account_id: number
  type: LedgerType | string
  amount_micros: number
  balance_after: number
  ref_type: string
  ref_id: string
  remark: string
  created_at: number
}

export interface LedgerQuery extends PageQuery {
  account_id?: number
  type?: string
  ref_type?: string
  from?: number
  to?: number
}

/* ------------------------------------------------------------------ */
/* §2.9 Reconcile                                                     */
/* ------------------------------------------------------------------ */

export interface ReconcileDetailRow {
  account_id: number
  v10_host_id: number
  usage_micros: number
  ledger_micros: number
  diff_micros: number
}

export interface ReconcileReport {
  day: string
  usage_cost_micros: number
  subscription_consumed_micros: number
  balance_consumed_micros: number
  ok: boolean
  diff_micros: number
  details: ReconcileDetailRow[]
}

/* ------------------------------------------------------------------ */
/* §2.10 Flags                                                        */
/* ------------------------------------------------------------------ */

export type FlagKey =
  | 'gateway_enabled'
  | 'billing_enforced'
  | 'module_autocreate_enabled'
  | 'recharge_enabled'

export interface FlagValue {
  key: FlagKey | string
  value: boolean
  /** Empty string = global scope; otherwise the account id as a string. */
  scope: string
  description?: string
  updated_at?: number
}

export interface FlagWriteRequest {
  value: boolean
  scope: string
}

/* ------------------------------------------------------------------ */
/* §2.11 System                                                       */
/* ------------------------------------------------------------------ */

export interface AuditEntry {
  id: number
  actor: string
  action: string
  target: string
  detail: string
  ip: string
  created_at: number
}

export interface Site {
  app_id: string
  name: string
  base_url: string
  status: number
  secret_tail: string
  /** Unix seconds; the previous secret stays valid until this moment. */
  prev_secret_expires_at: number | null
  created_at: number
}

export interface SiteCreateRequest {
  name: string
  base_url: string
}

export interface SiteCreateResponse {
  app_id: string
  /** Shown exactly once. */
  secret: string
  name: string
  base_url: string
  created_at: number
}

export interface SiteRotateResponse {
  app_id: string
  secret: string
  prev_expires_at: number
}

/** `GET /admin/v1/settings` — masked effective config, free-form. */
export interface SystemSettings {
  [key: string]: unknown
}

/**
 * Money ceilings and rate limits an operator may change at runtime.
 *
 * Money is micro-yuan (1 yuan = 1_000_000). Zero means "no cap", matching the
 * YAML semantics, so the form must not treat 0 as "unset".
 */
export interface RuntimeLimits {
  key_daily_micros: number
  account_monthly_micros: number
  global_daily_micros: number
  rpm: number
  tpm: number
  concurrency: number
}

/** Partial limits update: absent fields keep their current value. */
export type RuntimeLimitsPatch = Partial<RuntimeLimits>
