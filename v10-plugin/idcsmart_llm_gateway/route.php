<?php
/**
 * 插件自定义路由
 *
 * 三组路由与官方 idcsmart_ticket 4.2.2 完全同构（docs/v10-notes.md §4，plan 附录 D #1/#2/#3）：
 *   1. console/v1/api  : ParamFilter + Check（**免登录**）—— 只给"网关 -> V10 回调"用，必须自校验 HMAC
 *   2. console/v1      : ParamFilter + CheckHome + RejectRepeatRequest —— 会员中心
 *   3. {DIR_ADMIN}/v1  : ParamFilter + CheckAdmin —— 后台
 *
 * 每条路由的 ->append(['_plugin','_controller','_action']) 中 _action 为方法名的蛇形，
 * 带上后即可使用基类 PluginBaseController / PluginAdminBaseController 的通用方法。
 *
 * @author CZAPI
 * @version 2.0.0
 */
use think\facade\Route;

# ======================================================================
# 一、前台：免登录组（网关 -> V10 回调）
# ======================================================================
# ★ 安全提示：Check 中间件只表示"不校验登录态"，不代表已鉴权。
#   本组每个方法都必须在第一行调用 CallbackGuard::verify() 自校验 HMAC，
#   否则等于把接口裸奔在公网。参见 logic/CallbackGuard.php。
Route::group('console/v1/api', function () {
    # 额度耗尽通知（网关推理链路返回 402 时的旁路通知）
    Route::post('llm/callback/quota-exhausted', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@quotaExhausted")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'quota_exhausted']);
})
    ->middleware(\app\http\middleware\ParamFilter::class)
    ->middleware(\app\http\middleware\Check::class);

# ======================================================================
# 二、前台：会员中心（登录态）
# ======================================================================
Route::group('console/v1', function () {
    # ---------- 概览 / SSO ----------
    # 概览：余额 + 套餐额度 + 今日/本月用量 + 近30天趋势
    Route::get('llm/overview', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\IndexController@overview")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'index', '_action' => 'overview']);
    # ★ iframe 壳页面用：申请一次性 SSO 票据，拿到 sso_url 后赋给 iframe.src
    Route::get('llm/sso-ticket', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\IndexController@ssoTicket")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'index', '_action' => 'sso_ticket']);

    # ---------- Key 管理 ----------
    Route::get('llm/keys', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@keyList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'key_list']);
    Route::post('llm/keys', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@create")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'create']);
    Route::patch('llm/keys/:id', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@update")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'update']);
    Route::delete('llm/keys/:id', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@delete")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'delete']);

    # ---------- 模型 / 用量 ----------
    Route::get('llm/models', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\ModelController@modelList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'model', '_action' => 'model_list']);
    Route::get('llm/usage/summary', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\ModelController@usageSummary")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'model', '_action' => 'usage_summary']);
    Route::get('llm/usage/logs', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\ModelController@usageLogs")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'model', '_action' => 'usage_logs']);

    # ---------- 卡密 ----------
    Route::post('llm/redeem', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\RedeemController@redeem")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'redeem', '_action' => 'redeem']);
    Route::get('llm/redeem/records', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\RedeemController@records")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'redeem', '_action' => 'records']);
})
    ->middleware(\app\http\middleware\ParamFilter::class)
    ->middleware(\app\http\middleware\CheckHome::class)
    ->middleware(\app\http\middleware\RejectRepeatRequest::class);

# ======================================================================
# 三、后台：管理员（DIR_ADMIN 由系统常量注入）
# ======================================================================
Route::group(DIR_ADMIN . '/v1', function () {
    # ---------- 仪表盘 / 配置 ----------
    Route::get('llm/dashboard', "\\addon\\idcsmart_llm_gateway\\controller\\IndexController@dashboard")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'index', '_action' => 'dashboard']);
    Route::get('llm/config', "\\addon\\idcsmart_llm_gateway\\controller\\IndexController@getConfig")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'index', '_action' => 'get_config']);
    Route::post('llm/config', "\\addon\\idcsmart_llm_gateway\\controller\\IndexController@setConfig")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'index', '_action' => 'set_config']);

    # ---------- Key 检索 / 封禁 ----------
    Route::get('llm/keys', "\\addon\\idcsmart_llm_gateway\\controller\\KeyController@keyList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'key_list']);
    Route::post('llm/keys/sync', "\\addon\\idcsmart_llm_gateway\\controller\\KeyController@sync")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'sync']);
    Route::put('llm/keys/:id/status', "\\addon\\idcsmart_llm_gateway\\controller\\KeyController@updateStatus")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'update_status']);
    Route::delete('llm/keys/:id', "\\addon\\idcsmart_llm_gateway\\controller\\KeyController@delete")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'key', '_action' => 'delete']);

    # ---------- 渠道状态 / 模型 ----------
    Route::get('llm/channels/status', "\\addon\\idcsmart_llm_gateway\\controller\\ChannelController@status")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'channel', '_action' => 'status']);
    Route::get('llm/models', "\\addon\\idcsmart_llm_gateway\\controller\\ChannelController@modelList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'channel', '_action' => 'model_list']);
    Route::post('llm/models/sync', "\\addon\\idcsmart_llm_gateway\\controller\\ChannelController@syncModels")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'channel', '_action' => 'sync_models']);

    # ---------- 卡密批次 ----------
    Route::post('llm/redeem/batch', "\\addon\\idcsmart_llm_gateway\\controller\\RedeemController@createBatch")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'redeem', '_action' => 'create_batch']);
    Route::get('llm/redeem/batch', "\\addon\\idcsmart_llm_gateway\\controller\\RedeemController@batchList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'redeem', '_action' => 'batch_list']);
    Route::put('llm/redeem/batch/status', "\\addon\\idcsmart_llm_gateway\\controller\\RedeemController@setBatchStatus")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'redeem', '_action' => 'set_batch_status']);
    Route::get('llm/redeem/codes', "\\addon\\idcsmart_llm_gateway\\controller\\RedeemController@codeList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'redeem', '_action' => 'code_list']);
    Route::get('llm/redeem/txn', "\\addon\\idcsmart_llm_gateway\\controller\\RedeemController@txnList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'redeem', '_action' => 'txn_list']);

    # ---------- 推送队列 ----------
    Route::get('llm/pushlog', "\\addon\\idcsmart_llm_gateway\\controller\\PushLogController@pushLogList")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'pushlog', '_action' => 'push_log_list']);
    Route::post('llm/pushlog/retry-all', "\\addon\\idcsmart_llm_gateway\\controller\\PushLogController@retryAll")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'pushlog', '_action' => 'retry_all']);
    Route::post('llm/pushlog/:id/retry', "\\addon\\idcsmart_llm_gateway\\controller\\PushLogController@retry")
        ->append(['_plugin' => 'idcsmart_llm_gateway', '_controller' => 'pushlog', '_action' => 'retry']);
})
    ->middleware(\app\http\middleware\ParamFilter::class)
    ->middleware(\app\http\middleware\CheckAdmin::class);
