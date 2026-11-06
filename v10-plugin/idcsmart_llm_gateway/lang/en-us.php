<?php
/**
 * English language pack
 *
 * @author  CZAPI
 * @version 2.0.0
 */

return [
    # ---------- Common ----------
    'success_message'    => 'Success',
    'error_message'      => 'Request failed',
    'param_error'        => 'Invalid parameters',
    'delete_success'     => 'Deleted',
    'delete_fail'        => 'Delete failed',
    'update_success'     => 'Updated',

    # ---------- Navigation ----------
    'nav_plugin_addon_llm_gateway'        => 'LLM Gateway',
    'nav_plugin_addon_llm_gateway_index'  => 'Dashboard',
    'nav_plugin_addon_llm_gateway_key'    => 'API Keys',
    'nav_plugin_addon_llm_gateway_channel' => 'Channels & Models',
    'nav_plugin_addon_llm_gateway_redeem' => 'Redeem Batches',
    'nav_plugin_addon_llm_gateway_pushlog' => 'Push Queue',
    'nav_plugin_addon_llm_gateway_client' => 'LLM Gateway',

    # ---------- Admin auth rules ----------
    'auth_plugin_addon_llm_gateway'                => 'LLM Gateway',
    'auth_plugin_addon_llm_gateway_dashboard'      => 'View dashboard',
    'auth_plugin_addon_llm_gateway_key_view'       => 'View keys',
    'auth_plugin_addon_llm_gateway_key_manage'     => 'Suspend / revoke keys',
    'auth_plugin_addon_llm_gateway_channel'        => 'View channels and models',
    'auth_plugin_addon_llm_gateway_redeem_view'    => 'View redeem codes',
    'auth_plugin_addon_llm_gateway_redeem_manage'  => 'Create / disable redeem batches',
    'auth_plugin_addon_llm_gateway_pushlog'        => 'View / retry push queue',
    'auth_plugin_addon_llm_gateway_config'         => 'Edit plugin settings',

    # ---------- Clientarea auth rules ----------
    'clientarea_auth_plugin_addon_llm_gateway'       => 'LLM Gateway',
    'clientarea_auth_plugin_addon_llm_gateway_view'  => 'View',
    'clientarea_auth_plugin_addon_llm_gateway_manage' => 'Manage',
    'clientarea_auth_rule_plugin_addon_llm_gateway_overview'      => 'View overview',
    'clientarea_auth_rule_plugin_addon_llm_gateway_sso_ticket'    => 'Open console',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_list'      => 'Key list',
    'clientarea_auth_rule_plugin_addon_llm_gateway_model_list'    => 'Model list',
    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_summary' => 'Usage summary',
    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_logs'    => 'Usage logs',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_create'    => 'Create key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_update'    => 'Update key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_delete'    => 'Revoke key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem'        => 'Redeem code',
    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem_records' => 'Redemption history',

    # ---------- Login / connectivity ----------
    'llm_gw_not_login'            => 'Please sign in to the client area first',
    'llm_gw_gateway_unreachable'  => 'Gateway is temporarily unreachable, please retry later',
    'llm_gw_sso_failed'           => 'Failed to obtain the console sign-in ticket, please retry',

    # ---------- Keys ----------
    'llm_gw_key_not_exist'        => 'Key not found',
    'llm_gw_key_create_failed'    => 'Failed to create key',
    'llm_gw_key_update_failed'    => 'Failed to update key',
    'llm_gw_key_delete_failed'    => 'Failed to revoke key',
    'llm_gw_key_limit_reached'    => 'The key limit for this product has been reached',
    'llm_gw_key_id_require'       => 'Key ID is required',
    'llm_gw_key_id_invalid'       => 'Invalid key ID',
    'llm_gw_key_name_require'     => 'Please enter a key name',
    'llm_gw_key_name_max'         => 'Key name must not exceed 100 characters',
    'llm_gw_key_status_in'        => 'Key status must be active or suspended',
    'llm_gw_expires_at_invalid'   => 'Invalid expiration time',
    'llm_gw_no_field_to_update'   => 'Nothing to update',
    'llm_gw_client_id_require'    => 'Please select a client first',

    # ---------- Redeem codes ----------
    'llm_gw_redeem_code_require'  => 'Please enter a redeem code',
    'llm_gw_redeem_code_length'   => 'A redeem code must be 20 characters',
    'llm_gw_redeem_code_format'   => 'A redeem code may only contain letters and digits',
    'llm_gw_redeem_code_invalid'  => 'Redeem code not found, please check and retry',
    'llm_gw_redeem_code_used'     => 'This redeem code has already been used',
    'llm_gw_redeem_code_expired'  => 'This redeem code has expired',
    'llm_gw_redeem_batch_disabled' => 'This redeem batch has been disabled',
    'llm_gw_redeem_too_many_attempts' => 'Too many failed attempts today, please retry tomorrow',
    'llm_gw_redeem_processing'    => 'Processing, will retry automatically. Please do not submit again',
    'llm_gw_redeem_failed'        => 'Redemption failed, please contact support',
    'llm_gw_batch_count_require'  => 'Please enter the quantity',
    'llm_gw_batch_count_invalid'  => 'Quantity must be between 1 and 5000',
    'llm_gw_batch_generate_failed' => 'Failed to generate redeem codes, please retry',
    'llm_gw_batch_no_max'         => 'Batch number must not exceed 64 characters',
    'llm_gw_amount_require'       => 'Please enter the face value of each code',
    'llm_gw_amount_invalid'       => 'Face value must be greater than 0',

    # ---------- Push queue ----------
    'llm_gw_pushlog_not_exist'    => 'Push record not found',
    'llm_gw_pushlog_already_done' => 'This push already succeeded, no need to retry',
];
