<?php
/*
 * 自定义后台导航菜单（仅支持一、二级）
 *
 * 结构（源码实证，docs/v10-notes.md §6 / plan 附录 A）：
 *   一级：['name'(语言键), 'url', 'icon'(TDesign 图标名), 'in', 'child' => [二级...]]
 *   二级：['name'(语言键), 'url'(模板名，自动补 .html), 'in'(归属的一级导航 name), 'icon']
 *
 * 注意：
 *   - name 是**语言键**，必须在 lang/{zh-cn,zh-hk,en-us}.php 中定义，否则侧栏显示原始键名。
 *   - url 不加 .html，系统自动补；对应 template/admin/{url}.html
 *   - 系统会在"插件"导航下默认创建指向 plugin/idcsmart_llm_gateway/index.html 的入口，
 *     本文件额外把这几个页面挂到自己的分组下，便于运维直达。
 *
 * @author CZAPI
 * @version 2.0.0
 */
return [
    [
        # 一级导航：LLM 网关
        'name'  => 'nav_plugin_addon_llm_gateway',
        'url'   => '',
        'icon'  => 'chart-bubble',
        'in'    => '',
        'child' => [
            [
                # 仪表盘（= 插件默认入口 index.html）
                'name' => 'nav_plugin_addon_llm_gateway_index',
                'url'  => 'index',
                'in'   => 'nav_plugin_addon_llm_gateway',
                'icon' => 'dashboard',
            ],
            [
                # Key 检索 / 封禁
                'name' => 'nav_plugin_addon_llm_gateway_key',
                'url'  => 'key',
                'in'   => 'nav_plugin_addon_llm_gateway',
                'icon' => 'key',
            ],
            [
                # 渠道状态 / 模型目录
                'name' => 'nav_plugin_addon_llm_gateway_channel',
                'url'  => 'channel',
                'in'   => 'nav_plugin_addon_llm_gateway',
                'icon' => 'server',
            ],
            [
                # 卡密批次
                'name' => 'nav_plugin_addon_llm_gateway_redeem',
                'url'  => 'redeem',
                'in'   => 'nav_plugin_addon_llm_gateway',
                'icon' => 'gift',
            ],
            [
                # 推送队列（可靠投递可视化）
                'name' => 'nav_plugin_addon_llm_gateway_pushlog',
                'url'  => 'pushlog',
                'in'   => 'nav_plugin_addon_llm_gateway',
                'icon' => 'queue',
            ],
        ],
    ],
];
