/*
 * LLM 网关 —— 后台接口封装
 *
 * 约定（与官方 idcsmart_ticket 的 template/admin/api/*.js 一致）：
 *   - Axios 是 V10 后台的全局实例，已配置 baseURL（{站点}/{DIR_ADMIN}/v1）与 JWT 头
 *   - 因此这里只写相对路径：/llm/xxx
 *   - 返回值是 axios 响应，"业务成功"要判 res.data.status === 200
 */

/* ========== 仪表盘 / 配置 ========== */

// 仪表盘数据
function getLlmDashboard () {
  return Axios.get('/llm/dashboard')
}

// 读取插件配置（脱敏只读）
function getLlmConfig () {
  return Axios.get('/llm/config')
}

// 设置卸载策略（1 保留数据 / 0 彻底卸载）
function setLlmConfig (params) {
  return Axios.post('/llm/config', params)
}

/* ========== Key ========== */

// Key 列表（本地镜像）
function getLlmKeyList (params) {
  return Axios.get('/llm/keys', { params })
}

// 从网关刷新某客户的全部 Key
function syncLlmKeys (params) {
  return Axios.post('/llm/keys/sync', params)
}

// 封禁 / 解封
function updateLlmKeyStatus (id, params) {
  return Axios.put(`/llm/keys/${id}/status`, params)
}

// 吊销
function deleteLlmKey (id) {
  return Axios.delete(`/llm/keys/${id}`)
}

/* ========== 渠道 / 模型 ========== */

// 网关连通状态
function getLlmChannelStatus () {
  return Axios.get('/llm/channels/status')
}

// 可售模型目录
function getLlmModels (params) {
  return Axios.get('/llm/models', { params })
}

// 同步模型目录（只读核对）
function syncLlmModels () {
  return Axios.post('/llm/models/sync')
}

/* ========== 卡密 ========== */

// 生成批次（返回 codes 明文，仅此一次）
function createLlmRedeemBatch (params) {
  return Axios.post('/llm/redeem/batch', params)
}

// 批次汇总列表
function getLlmRedeemBatchList (params) {
  return Axios.get('/llm/redeem/batch', { params })
}

// 批次下的卡密明细（只含前缀）
function getLlmRedeemCodeList (params) {
  return Axios.get('/llm/redeem/codes', { params })
}

// 批次启用 / 禁用
function setLlmRedeemBatchStatus (params) {
  return Axios.put('/llm/redeem/batch/status', params)
}

// 兑换流水
function getLlmRedeemTxn (params) {
  return Axios.get('/llm/redeem/txn', { params })
}

/* ========== 推送队列 ========== */

// 队列列表
function getLlmPushLog (params) {
  return Axios.get('/llm/pushlog', { params })
}

// 手动重投单条
function retryLlmPushLog (id) {
  return Axios.post(`/llm/pushlog/${id}/retry`)
}

// 批量重投
function retryAllLlmPushLog (params) {
  return Axios.post('/llm/pushlog/retry-all', params)
}
