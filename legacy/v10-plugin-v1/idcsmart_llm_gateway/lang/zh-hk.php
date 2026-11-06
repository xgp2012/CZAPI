<?php
/**
 * 繁體中文語言包（香港）
 *
 * @author  CZAPI
 * @version 1.0.0
 */

return [
    # ---------- 通用 ----------
    'success_message'    => '請求成功',
    'error_message'      => '請求失敗',
    'param_error'        => '參數錯誤',
    'delete_success'     => '刪除成功',
    'delete_fail'        => '刪除失敗',
    'update_success'     => '編輯成功',

    # ---------- 導航 ----------
    'nav_plugin_addon_llm_gateway'        => 'LLM 網關',
    'nav_plugin_addon_llm_gateway_index'  => '儀表板',
    'nav_plugin_addon_llm_gateway_key'    => 'API Key',
    'nav_plugin_addon_llm_gateway_channel' => '渠道與模型',
    'nav_plugin_addon_llm_gateway_redeem' => '卡密批次',
    'nav_plugin_addon_llm_gateway_pushlog' => '推送隊列',
    'nav_plugin_addon_llm_gateway_client' => 'LLM 網關',

    # ---------- 後台權限點 ----------
    'auth_plugin_addon_llm_gateway'                => 'LLM 網關',
    'auth_plugin_addon_llm_gateway_dashboard'      => '查看儀表板',
    'auth_plugin_addon_llm_gateway_key_view'       => '查看 Key',
    'auth_plugin_addon_llm_gateway_key_manage'     => '封禁 / 吊銷 Key',
    'auth_plugin_addon_llm_gateway_channel'        => '查看渠道與模型',
    'auth_plugin_addon_llm_gateway_redeem_view'    => '查看卡密',
    'auth_plugin_addon_llm_gateway_redeem_manage'  => '生成 / 停用卡密批次',
    'auth_plugin_addon_llm_gateway_pushlog'        => '查看 / 重投推送隊列',
    'auth_plugin_addon_llm_gateway_config'         => '修改插件配置',

    # ---------- 前台權限點 ----------
    'clientarea_auth_plugin_addon_llm_gateway'       => 'LLM 網關',
    'clientarea_auth_plugin_addon_llm_gateway_view'  => '查看',
    'clientarea_auth_plugin_addon_llm_gateway_manage' => '管理',
    'clientarea_auth_rule_plugin_addon_llm_gateway_overview'      => '查看概覽',
    'clientarea_auth_rule_plugin_addon_llm_gateway_sso_ticket'    => '進入控制台',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_list'      => 'Key 列表',
    'clientarea_auth_rule_plugin_addon_llm_gateway_model_list'    => '模型列表',
    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_summary' => '用量彙總',
    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_logs'    => '用量明細',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_create'    => '建立 Key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_update'    => '修改 Key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_delete'    => '吊銷 Key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem'        => '卡密兌換',
    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem_records' => '兌換記錄',

    # ---------- 登入 / 網關連通 ----------
    'llm_gw_not_login'            => '請先登入會員中心',
    'llm_gw_gateway_unreachable'  => '網關暫時無法連線，請稍後重試或聯絡管理員',
    'llm_gw_sso_failed'           => '取得控制台登入票據失敗，請稍後重試',

    # ---------- Key ----------
    'llm_gw_key_not_exist'        => 'Key 不存在',
    'llm_gw_key_create_failed'    => '建立 Key 失敗',
    'llm_gw_key_update_failed'    => '修改 Key 失敗',
    'llm_gw_key_delete_failed'    => '吊銷 Key 失敗',
    'llm_gw_key_limit_reached'    => '該產品可建立的 Key 數量已達上限',
    'llm_gw_key_id_require'       => '缺少 Key ID',
    'llm_gw_key_id_invalid'       => 'Key ID 不合法',
    'llm_gw_key_name_require'     => '請輸入 Key 名稱',
    'llm_gw_key_name_max'         => 'Key 名稱不超過 100 個字元',
    'llm_gw_key_status_in'        => 'Key 狀態只能是 active 或 suspended',
    'llm_gw_expires_at_invalid'   => '過期時間不合法',
    'llm_gw_no_field_to_update'   => '沒有需要修改的內容',
    'llm_gw_client_id_require'    => '請先選擇客戶',

    # ---------- 卡密 ----------
    'llm_gw_redeem_code_require'  => '請輸入卡密',
    'llm_gw_redeem_code_length'   => '卡密應為 20 位字元',
    'llm_gw_redeem_code_format'   => '卡密只能包含字母和數字',
    'llm_gw_redeem_code_invalid'  => '卡密不存在，請核對後重試',
    'llm_gw_redeem_code_used'     => '該卡密已被兌換',
    'llm_gw_redeem_code_expired'  => '該卡密已過期',
    'llm_gw_redeem_batch_disabled' => '該批次卡密已被停用',
    'llm_gw_redeem_too_many_attempts' => '今日兌換失敗次數過多，請明天再試或聯絡客服',
    'llm_gw_redeem_processing'    => '處理中，稍後自動重試，請勿重複提交',
    'llm_gw_redeem_failed'        => '兌換失敗，請聯絡客服',
    'llm_gw_batch_count_require'  => '請輸入生成數量',
    'llm_gw_batch_count_invalid'  => '生成數量需在 1-5000 之間',
    'llm_gw_batch_generate_failed' => '生成卡密失敗，請重試',
    'llm_gw_batch_no_max'         => '批次號不超過 64 個字元',
    'llm_gw_amount_require'       => '請輸入每張卡密的面額',
    'llm_gw_amount_invalid'       => '面額必須大於 0',

    # ---------- 推送隊列 ----------
    'llm_gw_pushlog_not_exist'    => '推送記錄不存在',
    'llm_gw_pushlog_already_done' => '該推送已經成功，無需重投',
];
