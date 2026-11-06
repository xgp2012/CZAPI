<?php
/**
 * 易支付网关 —— 后台配置表单
 *
 * 官方《支付接口开发》文档：在支付接口根目录放 config.php，后台会在「插件设置」里
 * 自动渲染成表单，不需要自己写配置页。
 *
 * 字段说明见各键的 tip。pid / key 由易支付商户后台提供；api_url 是易支付站点根地址
 * （例如 https://pay.example.com），插件会自动拼 /submit.php。
 *
 * @author  CZAPI
 * @version 1.0.0
 */

return [
    'pid'          => [
        'title' => '商户 ID（pid）',
        'type'  => 'text',
        'value' => '',
        'tip'   => '易支付商户后台的商户 ID，纯数字',
        'size'  => 200,
    ],
    'key'          => [
        'title' => '商户密钥（key）',
        'type'  => 'password',
        'value' => '',
        'tip'   => '易支付商户后台的密钥，仅用于服务端签名，保存后不应完整回显',
        'size'  => 400,
    ],
    'api_url'      => [
        'title' => '接口地址',
        'type'  => 'text',
        'value' => '',
        'tip'   => '易支付站点根地址，不要带结尾斜杠。例如 https://pay.example.com（插件会自动拼 /submit.php）',
        'size'  => 400,
    ],
    'default_type' => [
        'title'   => '默认支付方式',
        'type'    => 'select',
        'options' => [
            'alipay' => '支付宝',
            'wxpay'  => '微信支付',
            'qqpay'  => 'QQ 钱包',
        ],
        'value'   => 'alipay',
        'tip'     => '当 V10 未传具体支付方式时使用（多数场景下 V10 会传，无需依赖此项）',
    ],
];
