/**
 * Admin API (`/admin/v1`) — docs/console-api-v1.md §2.
 */

import { del, get, isMock, patch, post, put } from './client'
import { mockAdmin } from './mock'
import type {
  Account,
  AccountDetail,
  AdjustAccountRequest,
  AdjustAccountResponse,
  AdminKey,
  AdminModel,
  AdminSubscription,
  AuditEntry,
  Channel,
  ChannelSyncResult,
  ChannelTestResult,
  ChannelWrite,
  Dashboard,
  FlagValue,
  LedgerEntry,
  LedgerQuery,
  LoginResponse,
  Paged,
  Plan,
  ReconcileReport,
  Site,
  SiteCreateRequest,
  SiteCreateResponse,
  SiteRotateResponse,
  SubscriptionActionResponse,
  RuntimeLimitsPatch,
  SystemSettings,
} from '@/types/api'

export const adminApi = {
  /* §2.1 ---------------------------------------------------------- */
  async login(username: string, password: string): Promise<LoginResponse> {
    if (isMock()) return mockAdmin.login(username, password)
    return post<LoginResponse>('/admin/v1/login', { username, password })
  },

  async logout(): Promise<void> {
    if (isMock()) return
    await post<null>('/admin/v1/logout')
  },

  /* §2.2 ---------------------------------------------------------- */
  async dashboard(): Promise<Dashboard> {
    if (isMock()) return mockAdmin.dashboard()
    return get<Dashboard>('/admin/v1/dashboard')
  },

  /* §2.3 channels ------------------------------------------------- */
  async listChannels(): Promise<Channel[]> {
    if (isMock()) return mockAdmin.listChannels()
    const data = await get<Paged<Channel> | Channel[]>('/admin/v1/channels')
    return Array.isArray(data) ? data : data.items
  },

  async getChannel(id: number): Promise<Channel> {
    if (isMock()) return mockAdmin.getChannel(id)
    return get<Channel>(`/admin/v1/channels/${id}`)
  },

  async createChannel(payload: ChannelWrite): Promise<Channel> {
    if (isMock()) return mockAdmin.createChannel(payload)
    return post<Channel>('/admin/v1/channels', payload)
  },

  async updateChannel(id: number, payload: Partial<ChannelWrite>): Promise<Channel> {
    if (isMock()) return mockAdmin.updateChannel(id, payload)
    return patch<Channel>(`/admin/v1/channels/${id}`, payload)
  },

  async deleteChannel(id: number): Promise<{ channel_id: number }> {
    if (isMock()) return mockAdmin.deleteChannel(id)
    return del<{ channel_id: number }>(`/admin/v1/channels/${id}`)
  },

  async testChannel(id: number): Promise<ChannelTestResult> {
    if (isMock()) return mockAdmin.testChannel(id)
    return post<ChannelTestResult>(`/admin/v1/channels/${id}/test`)
  },

  async syncChannelModels(id: number): Promise<ChannelSyncResult> {
    if (isMock()) return mockAdmin.syncChannelModels(id)
    return post<ChannelSyncResult>(`/admin/v1/channels/${id}/sync-models`)
  },

  /* §2.4 models --------------------------------------------------- */
  async listModels(): Promise<AdminModel[]> {
    if (isMock()) return mockAdmin.listModels()
    const data = await get<Paged<AdminModel> | AdminModel[]>('/admin/v1/models')
    return Array.isArray(data) ? data : data.items
  },

  async upsertModel(payload: AdminModel): Promise<AdminModel> {
    if (isMock()) return mockAdmin.upsertModel(payload)
    return post<AdminModel>('/admin/v1/models', payload)
  },

  async updateModel(name: string, payload: Partial<AdminModel>): Promise<AdminModel> {
    if (isMock()) {
      const current = await mockAdmin.listModels()
      const found = current.find((m) => m.model_name === name)
      return mockAdmin.upsertModel({ ...(found as AdminModel), ...payload })
    }
    return patch<AdminModel>(`/admin/v1/models/${encodeURIComponent(name)}`, payload)
  },

  async deleteModel(name: string): Promise<{ model_name: string }> {
    if (isMock()) return mockAdmin.deleteModel(name)
    return del<{ model_name: string }>(`/admin/v1/models/${encodeURIComponent(name)}`)
  },

  /* §2.5 plans ---------------------------------------------------- */
  async listPlans(): Promise<Plan[]> {
    if (isMock()) return mockAdmin.listPlans()
    const data = await get<Paged<Plan> | Plan[]>('/admin/v1/plans')
    return Array.isArray(data) ? data : data.items
  },

  async upsertPlan(payload: Plan): Promise<Plan> {
    if (isMock()) return mockAdmin.upsertPlan(payload)
    return post<Plan>('/admin/v1/plans', payload)
  },

  async updatePlan(code: string, payload: Partial<Plan>): Promise<Plan> {
    if (isMock()) {
      const plans = await mockAdmin.listPlans()
      const found = plans.find((p) => p.plan_code === code)
      return mockAdmin.upsertPlan({ ...(found as Plan), ...payload })
    }
    return patch<Plan>(`/admin/v1/plans/${encodeURIComponent(code)}`, payload)
  },

  async deletePlan(code: string): Promise<{ plan_code: string }> {
    if (isMock()) return mockAdmin.deletePlan(code)
    return del<{ plan_code: string }>(`/admin/v1/plans/${encodeURIComponent(code)}`)
  },

  /* §2.6 accounts & keys ------------------------------------------ */
  async listAccounts(q: string, page = 1, page_size = 20): Promise<Paged<Account>> {
    if (isMock()) return mockAdmin.listAccounts(q, page, page_size)
    return get<Paged<Account>>('/admin/v1/accounts', { params: { q, page, page_size } })
  },

  async getAccount(id: number): Promise<AccountDetail> {
    if (isMock()) return mockAdmin.getAccount(id)
    return get<AccountDetail>(`/admin/v1/accounts/${id}`)
  },

  async adjustAccount(id: number, payload: AdjustAccountRequest): Promise<AdjustAccountResponse> {
    if (isMock()) return mockAdmin.adjustAccount(id, payload)
    return post<AdjustAccountResponse>(`/admin/v1/accounts/${id}/adjust`, payload)
  },

  async updateAccount(id: number, payload: { status?: number; allow_negative?: boolean }): Promise<Account> {
    if (isMock()) return mockAdmin.updateAccount(id, payload)
    return patch<Account>(`/admin/v1/accounts/${id}`, payload)
  },

  async searchKeys(q: string, status: string, account_id: number | undefined, page = 1, page_size = 20): Promise<Paged<AdminKey>> {
    if (isMock()) return mockAdmin.searchKeys(q, status, account_id, page, page_size)
    return get<Paged<AdminKey>>('/admin/v1/keys', { params: { q, status, account_id, page, page_size } })
  },

  async setKeyStatus(id: number, status: 'active' | 'suspended'): Promise<AdminKey> {
    if (isMock()) return mockAdmin.banKey(id, status)
    return patch<AdminKey>(`/admin/v1/keys/${id}`, { status })
  },

  /* §2.7 subscriptions -------------------------------------------- */
  async listSubscriptions(status: string, page = 1, page_size = 20): Promise<Paged<AdminSubscription>> {
    if (isMock()) return mockAdmin.listSubscriptions(status, page, page_size)
    return get<Paged<AdminSubscription>>('/admin/v1/subscriptions', { params: { status, page, page_size } })
  },

  async getSubscription(id: number): Promise<AdminSubscription> {
    if (isMock()) return mockAdmin.getSubscription(id)
    return get<AdminSubscription>(`/admin/v1/subscriptions/${id}`)
  },

  async subscriptionAction(id: number, action: 'suspend' | 'resume' | 'reset-quota'): Promise<SubscriptionActionResponse> {
    if (isMock()) return mockAdmin.subscriptionAction(id, action)
    return post<SubscriptionActionResponse>(`/admin/v1/subscriptions/${id}/${action}`)
  },

  /* §2.8 ledger --------------------------------------------------- */
  async listLedger(query: LedgerQuery): Promise<Paged<LedgerEntry>> {
    if (isMock()) return mockAdmin.listLedger(query)
    return get<Paged<LedgerEntry>>('/admin/v1/ledger', { params: query })
  },

  /* §2.9 reconcile ------------------------------------------------ */
  async reconcile(day: string): Promise<ReconcileReport> {
    if (isMock()) return mockAdmin.reconcile(day)
    return get<ReconcileReport>('/admin/v1/reconcile', { params: { day } })
  },

  /* §2.10 flags --------------------------------------------------- */
  async listFlags(): Promise<FlagValue[]> {
    if (isMock()) return mockAdmin.listFlags()
    const data = await get<FlagValue[] | { items: FlagValue[] }>('/admin/v1/flags')
    return Array.isArray(data) ? data : data.items
  },

  async setFlag(key: string, value: boolean, scope: string): Promise<FlagValue> {
    if (isMock()) return mockAdmin.setFlag(key, value, scope)
    return put<FlagValue>(`/admin/v1/flags/${key}`, { value, scope })
  },

  /* §2.11 system -------------------------------------------------- */
  async settings(): Promise<SystemSettings> {
    if (isMock()) return mockAdmin.settings()
    return get<SystemSettings>('/admin/v1/settings')
  },

  /** Applies a partial limits update; changes take effect immediately. */
  async updateLimits(payload: RuntimeLimitsPatch): Promise<SystemSettings> {
    return patch<SystemSettings>('/admin/v1/settings/limits', payload)
  },

  /** Changes the signed-in administrator's own password. */
  async changePassword(current_password: string, new_password: string): Promise<{ message: string }> {
    return post<{ message: string }>('/admin/v1/password', { current_password, new_password })
  },

  async listAudit(page = 1, page_size = 20): Promise<Paged<AuditEntry>> {
    if (isMock()) return mockAdmin.listAudit(page, page_size)
    return get<Paged<AuditEntry>>('/admin/v1/audit', { params: { page, page_size } })
  },

  async listSites(): Promise<Site[]> {
    if (isMock()) return mockAdmin.listSites()
    const data = await get<Paged<Site> | Site[]>('/admin/v1/sites')
    return Array.isArray(data) ? data : data.items
  },

  async createSite(payload: SiteCreateRequest): Promise<SiteCreateResponse> {
    if (isMock()) return mockAdmin.createSite(payload)
    return post<SiteCreateResponse>('/admin/v1/sites', payload)
  },

  async rotateSiteSecret(app_id: string): Promise<SiteRotateResponse> {
    if (isMock()) return mockAdmin.rotateSiteSecret(app_id)
    return post<SiteRotateResponse>(`/admin/v1/sites/${app_id}/rotate-secret`)
  },
}
