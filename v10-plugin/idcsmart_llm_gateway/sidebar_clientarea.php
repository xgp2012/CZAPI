<?php
/*
 * 自定义前台（会员中心）导航菜单（仅支持一、二级）
 *
 * 结构与官方 idcsmart_ticket 4.2.2 的 sidebar_clientarea.php 完全一致（源码实证）：
 *   ['name' => 语言键, 'url' => 模板名（自动补 .html）, 'icon' => '', 'child' => []]
 *
 * 模板文件位置：template/clientarea/{pc|mobile}/{主题}/llm.html
 * 该页面是一个 iframe 壳，内容由 js/llm.js 动态写入（见 README「前台页面」）。
 *
 * @author CZAPI
 * @version 2.0.0
 */
return [
    [
        'name'  => 'nav_plugin_addon_llm_gateway_client', # 语言键，需在 lang/ 下定义
        'url'   => 'llm',                                  # 自动补 .html -> llm.html
        'icon'  => '',                                     # 图标（前台可留空）
        'child' => [],                                     # 二级菜单
    ],
];
