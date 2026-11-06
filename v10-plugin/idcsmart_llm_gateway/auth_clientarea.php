<?php
/*
 * 前台（会员中心）权限点定义
 *
 * 结构与官方 idcsmart_ticket 4.2.2 的 auth_clientarea.php 完全一致（源码实证）：
 *   [
 *     'title' => 语言键,
 *     'url'   => 模板名,
 *     'child' => [
 *        [
 *          'title'          => 语言键（这一组权限的名称）,
 *          'url'            => '',
 *          'auth_rule'      => ['控制器::方法', ...],
 *          'auth_rule_title'=> [每个方法对应的语言键...]
 *        ]
 *     ]
 *   ]
 *
 * ★ 源码注释明确：要使用二级权限，**插件后台控制器必须继承
 *   app\event\controller\PluginAdminBaseController**。
 *
 * @author CZAPI
 * @version 2.0.0
 */
return [
    [
        'title' => 'clientarea_auth_plugin_addon_llm_gateway',
        'url'   => 'llm',
        'child' => [
            # 查看：概览 / Key 列表 / 模型 / 用量
            [
                'title'           => 'clientarea_auth_plugin_addon_llm_gateway_view',
                'url'             => '',
                'auth_rule'       => [
                    'IndexController::overview',
                    'IndexController::ssoTicket',
                    'KeyController::keyList',
                    'ModelController::modelList',
                    'ModelController::usageSummary',
                    'ModelController::usageLogs',
                ],
                'auth_rule_title' => [
                    'clientarea_auth_rule_plugin_addon_llm_gateway_overview',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_sso_ticket',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_key_list',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_model_list',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_summary',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_usage_logs',
                ],
            ],
            # 管理：Key 增删改 + 卡密兑换
            [
                'title'           => 'clientarea_auth_plugin_addon_llm_gateway_manage',
                'url'             => '',
                'auth_rule'       => [
                    'KeyController::create',
                    'KeyController::update',
                    'KeyController::delete',
                    'RedeemController::redeem',
                    'RedeemController::records',
                ],
                'auth_rule_title' => [
                    'clientarea_auth_rule_plugin_addon_llm_gateway_key_create',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_key_update',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_key_delete',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem',
                    'clientarea_auth_rule_plugin_addon_llm_gateway_redeem_records',
                ],
            ],
        ],
    ],
];
