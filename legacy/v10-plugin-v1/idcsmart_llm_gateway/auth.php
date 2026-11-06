<?php
/*
 * 后台权限点定义
 *
 * 结构（源码实证，见官方 idcsmart_ticket 4.2.2 的 auth.php）：
 *   [
 *     'title'       => 权限标识（唯一，同时是语言文件里的键）,
 *     'url'         => 权限对应模板名（存在子权限时一般留空）,
 *     'description' => 权限描述,
 *     'parent'      => 父权限标识（可选，挂到已有权限下）,
 *     'child'       => [ 子权限... ]
 *   ]
 * 子权限的 auth_rule 写法为 '命名空间\类::方法'，前端用 v-permission="'权限标识'" 控制按钮显隐。
 *
 * 注意：系统会默认插入名称为插件名的一级权限，本文件只定义二级/三级权限。
 *
 * @author CZAPI
 * @version 1.0.0
 */
return [
    [
        'title'       => 'auth_plugin_addon_llm_gateway',
        'url'         => '',
        'description' => 'LLM 网关',
        'child'       => [
            [
                # 仪表盘
                'title'       => 'auth_plugin_addon_llm_gateway_dashboard',
                'url'         => 'index',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\IndexController::dashboard',
                    'addon\idcsmart_llm_gateway\controller\IndexController::getConfig',
                ],
                'description' => '查看仪表盘',
            ],
            [
                # Key 检索与封禁
                'title'       => 'auth_plugin_addon_llm_gateway_key_view',
                'url'         => 'key',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\KeyController::keyList',
                    'addon\idcsmart_llm_gateway\controller\KeyController::sync',
                ],
                'description' => '查看 Key',
            ],
            [
                # Key 封禁/吊销（危险操作，单独授权）
                'title'       => 'auth_plugin_addon_llm_gateway_key_manage',
                'url'         => '',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\KeyController::updateStatus',
                    'addon\idcsmart_llm_gateway\controller\KeyController::delete',
                ],
                'description' => '封禁 / 吊销 Key',
            ],
            [
                # 渠道状态与模型目录
                'title'       => 'auth_plugin_addon_llm_gateway_channel',
                'url'         => 'channel',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\ChannelController::status',
                    'addon\idcsmart_llm_gateway\controller\ChannelController::modelList',
                    'addon\idcsmart_llm_gateway\controller\ChannelController::syncModels',
                ],
                'description' => '查看渠道与模型',
            ],
            [
                # 卡密批次生成（含明文导出，必须严格控制）
                'title'       => 'auth_plugin_addon_llm_gateway_redeem_view',
                'url'         => 'redeem',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\RedeemController::batchList',
                    'addon\idcsmart_llm_gateway\controller\RedeemController::codeList',
                    'addon\idcsmart_llm_gateway\controller\RedeemController::txnList',
                ],
                'description' => '查看卡密',
            ],
            [
                'title'       => 'auth_plugin_addon_llm_gateway_redeem_manage',
                'url'         => '',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\RedeemController::createBatch',
                    'addon\idcsmart_llm_gateway\controller\RedeemController::setBatchStatus',
                ],
                'description' => '生成 / 禁用卡密批次',
            ],
            [
                # 推送队列（手动重投涉及金额，单独授权）
                'title'       => 'auth_plugin_addon_llm_gateway_pushlog',
                'url'         => 'pushlog',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\PushLogController::pushLogList',
                    'addon\idcsmart_llm_gateway\controller\PushLogController::retry',
                    'addon\idcsmart_llm_gateway\controller\PushLogController::retryAll',
                ],
                'description' => '查看 / 重投推送队列',
            ],
            [
                # 插件配置
                'title'       => 'auth_plugin_addon_llm_gateway_config',
                'url'         => '',
                'auth_rule'   => [
                    'addon\idcsmart_llm_gateway\controller\IndexController::setConfig',
                ],
                'description' => '修改插件配置',
            ],
        ],
    ],
];
