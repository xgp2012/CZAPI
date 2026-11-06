<?php
/**
 * 简体中文语言包
 *
 * 键名统一带 llm_gw_ 前缀，避免与系统语言包冲突（docs/v10-notes.md §8）。
 * 系统内置键（success_message / error_message / param_error）在插件里再声明一次，
 * 与官方 idcsmart_ticket 的 lang/zh-cn.php 做法一致。
 *
 * @author  CZAPI
 * @version 2.0.0
 */

return [
    # ---------- 通用 ----------
    'success_message'    => '请求成功',
    'error_message'      => '请求失败',
    'param_error'        => '参数错误',
    'delete_success'     => '删除成功',
    'delete_fail'        => '删除失败',
    'update_success'     => '编辑成功',

    # ---------- 导航（sidebar.php / sidebar_clientarea.php） ----------
    'nav_plugin_addon_llm_gateway'        => 'LLM 网关',
    'nav_plugin_addon_llm_gateway_index'  => '仪表盘',
    'nav_plugin_addon_llm_gateway_key'    => 'API Key',
    'nav_plugin_addon_llm_gateway_channel' => '渠道与模型',
    'nav_plugin_addon_llm_gateway_redeem' => '卡密批次',
    'nav_plugin_addon_llm_gateway_pushlog' => '推送队列',
    'nav_plugin_addon_llm_gateway_client' => 'LLM 网关',

    # ---------- 后台权限点（auth.php） ----------
    'auth_plugin_addon_llm_gateway'                => 'LLM 网关',
    'auth_plugin_addon_llm_gateway_dashboard'      => '查看仪表盘',
    'auth_plugin_addon_llm_gateway_key_view'       => '查看 Key',
    'auth_plugin_addon_llm_gateway_key_manage'     => '封禁 / 吊销 Key',
    'auth_plugin_addon_llm_gateway_channel'        => '查看渠道与模型',
    'auth_plugin_addon_llm_gateway_redeem_view'    => '查看卡密',
    'auth_plugin_addon_llm_gateway_redeem_manage'  => '生成 / 禁用卡密批次',
    'auth_plugin_addon_llm_gateway_pushlog'        => '查看 / 重投推送队列',
    'auth_plugin_addon_llm_gateway_config'         => '修改插件配置',

    # ---------- 前台权限点（auth_clientarea.php） ----------
    'clientarea_auth_plugin_addon_llm_gateway'       => 'LLM 网关',
    'clientarea_auth_plugin_addon_llm_gateway_view'  => '查看',
    'clientarea_auth_plugin_addon_llm_gateway_manage' => '管理',
    'clientarea_auth_rule_plugin_addon_llm_gateway_overview'      => '查看概览',
    'clientarea_auth_rule_plugin_addon_llm_gateway_sso_ticket'    => '进入控制台',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_list'      => 'Key 列表',
    'clientarea_auth_rule_plugin_addon_llm_gateway_model_list'    => '模型列表',
    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_summary' => '用量汇总',
    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_logs'    => '用量明细',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_create'    => '创建 Key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_update'    => '修改 Key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_key_delete'    => '吊销 Key',
    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem'        => '卡密兑换',
    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem_records' => '兑换记录',

    # ---------- 登录 / 网关连通 ----------
    'llm_gw_not_login'            => '请先登录会员中心',
    'llm_gw_gateway_unreachable'  => '网关暂时不可达，请稍后重试或联系管理员',
    'llm_gw_sso_failed'           => '获取控制台登录票据失败，请稍后重试',

    # ---------- Key ----------
    'llm_gw_key_not_exist'        => 'Key 不存在',
    'llm_gw_key_create_failed'    => '创建 Key 失败',
    'llm_gw_key_update_failed'    => '修改 Key 失败',
    'llm_gw_key_delete_failed'    => '吊销 Key 失败',
    'llm_gw_key_limit_reached'    => '该产品可创建的 Key 数量已达上限',
    'llm_gw_key_id_require'       => '缺少 Key ID',
    'llm_gw_key_id_invalid'       => 'Key ID 不合法',
    'llm_gw_key_name_require'     => '请输入 Key 名称',
    'llm_gw_key_name_max'         => 'Key 名称不超过 100 个字符',
    'llm_gw_key_status_in'        => 'Key 状态只能是 active 或 suspended',
    'llm_gw_expires_at_invalid'   => '过期时间不合法',
    'llm_gw_no_field_to_update'   => '没有需要修改的内容',
    'llm_gw_client_id_require'    => '请先选择客户',

    # ---------- 卡密 ----------
    'llm_gw_redeem_code_require'  => '请输入卡密',
    'llm_gw_redeem_code_length'   => '卡密应为 20 位字符',
    'llm_gw_redeem_code_format'   => '卡密只能包含字母和数字',
    'llm_gw_redeem_code_invalid'  => '卡密不存在，请核对后重试',
    'llm_gw_redeem_code_used'     => '该卡密已被兑换',
    'llm_gw_redeem_code_expired'  => '该卡密已过期',
    'llm_gw_redeem_batch_disabled' => '该批次卡密已被停用',
    'llm_gw_redeem_too_many_attempts' => '今日兑换失败次数过多，请明天再试或联系客服',
    'llm_gw_redeem_processing'    => '处理中，稍后自动重试，请勿重复提交',
    'llm_gw_redeem_failed'        => '兑换失败，请联系客服',
    'llm_gw_batch_count_require'  => '请输入生成数量',
    'llm_gw_batch_count_invalid'  => '生成数量需在 1-5000 之间',
    'llm_gw_batch_generate_failed' => '生成卡密失败，请重试',
    'llm_gw_batch_no_max'         => '批次号不超过 64 个字符',
    'llm_gw_amount_require'       => '请输入每张卡密的面额',
    'llm_gw_amount_invalid'       => '面额必须大于 0',

    # ---------- 推送队列 ----------
    'llm_gw_pushlog_not_exist'    => '推送记录不存在',
    'llm_gw_pushlog_already_done' => '该推送已经成功，无需重投',
];
