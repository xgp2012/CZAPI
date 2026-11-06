<?php
/**
 * CZAPI LLM 网关插件 —— 配置表单定义
 *
 * V10 后台「插件 -> 配置」页会依据本文件自动渲染表单，插件不需要（也不要）自己写配置页。
 * 表单结构（源码实证，见 docs/v10-notes.md §7）：
 *   '配置键' => ['title' => '标题', 'type' => 'text|password|textarea|checkbox|radio|select',
 *                'value' => 默认值, 'tip' => '帮助提示']
 *
 * 读取方式：LlmGatewayLogic::get()，内部 = config.php 默认值 + idcsmart_plugin.config(JSON) 覆盖。
 *
 * @author  CZAPI
 * @version 2.0.0
 */

return [
    # 网关根地址，不带结尾斜杠
    'gateway_base_url'     => [
        'title' => '网关地址',
        'type'  => 'text',
        'value' => 'https://api.example.com',
        'tip'   => 'CZAPI 网关根地址，不带结尾斜杠。内部接口前缀 /internal/v10 由插件自动拼接。',
    ],
    # 网关按 V10 站点签发的 App ID
    'app_id'               => [
        'title' => 'App ID',
        'type'  => 'text',
        'value' => '',
        'tip'   => '由网关后台「站点」中注册本站点后签发。',
    ],
    # 网关按 V10 站点签发的 App Secret
    'app_secret'           => [
        'title' => 'App Secret',
        'type'  => 'password',
        'value' => '',
        'tip'   => '仅显示尾 6 位。用于 /internal/v10 请求签名（HMAC-SHA256）。',
    ],
    # 旧 secret，轮换期双活（plan §8.7 密钥轮换）
    'app_secret_prev'      => [
        'title' => 'App Secret（上一版）',
        'type'  => 'password',
        'value' => '',
        'tip'   => '密钥轮换期间保留的旧 secret，仅用于校验网关回调，7 天后清空。',
    ],
    # V10 商品 -> 网关套餐 ID 映射
    'plan_map'             => [
        'title' => '套餐映射',
        'type'  => 'textarea',
        'value' => "{\n  \"12\": 1,\n  \"13\": 2,\n  \"14\": 3\n}",
        'tip'   => 'JSON：{"V10商品ID": 网关套餐ID(数字)}。值是网关 subscription_plans.id，'
                 . '可在网关后台「套餐」页或 /api/v10/admin/site-options 查到。模块开通时用它把商品映射到套餐。',
    ],
    # 默认计费模式
    'default_billing_mode' => [
        'title' => '默认计费模式',
        'type'  => 'select',
        'value' => 'hybrid',
        'tip'   => 'subscription=仅套餐额度；balance=仅余额；hybrid=先套餐后余额。商品配置了 billing_mode 时以商品为准。',
    ],
    # 每产品可创建 Key 上限
    'max_keys_per_host'    => [
        'title' => '每产品 Key 上限',
        'type'  => 'text',
        'value' => 5,
        'tip'   => '单个 V10 产品（host）最多可创建多少把 Key；网关侧另有 plan.max_keys 兜底。',
    ],
    # 内部接口调用超时
    'timeout_ms'           => [
        'title' => '调用超时(ms)',
        'type'  => 'text',
        'value' => 5000,
        'tip'   => '插件 -> 网关 内部接口的 cURL 超时，单位毫秒。',
    ],
    # 免登录回调组 HMAC 共享密钥
    'callback_secret'      => [
        'title' => '回调签名密钥',
        'type'  => 'password',
        'value' => '',
        'tip'   => 'console/v1/api 组（网关 -> V10 回调）的 HMAC 共享密钥。留空则该组全部拒绝，防止裸露。',
    ],
    # 卸载策略
    'keep_data_on_uninstall' => [
        'title' => '卸载时保留数据',
        'type'  => 'select',
        'value' => '1',
        'tip'   => '1=保守卸载（只删配置与导航，保留 Key/卡密/流水/推送/用量数据）；0=彻底卸载（连表一起删除，不可恢复）。',
    ],
];
