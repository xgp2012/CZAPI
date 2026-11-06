# V10 插件开发笔记（基于官方 idcsmart_ticket 4.2.2 源码实测）

> 来源：`C:\Users\Administrator\Downloads\idcsmart_ticket_wGssZ.tar.gz`（官方工单插件，v4.2.2）
> 用途：作为本项目的"权威样例"。文档站与真实源码不一致的地方，**以本文件为准**。
> 结论标注：✅ = 源码实证；⚠️ = 文档说法 / 待实例验证。

---

## 1. 安装包与目录结构 ✅

安装包是 `tar.gz`，解开后是**单个插件目录**，直接放进 `public/plugins/addon/` 即可。

```
idcsmart_ticket/                     ← 目录名 = 插件标识（小写下划线、字母开头）
├── IdcsmartTicket.php               ← 主类，类名 = 目录名大驼峰，namespace addon\idcsmart_ticket
├── route.php                        ← 自定义路由（前台/后台）
├── sidebar.php                      ← 后台导航（本样例被注释掉了，需要时自行开启）
├── sidebar_clientarea.php           ← ★ 前台（会员中心）导航，源码里是启用的
├── auth.php                         ← 后台权限点定义（配合 v-permission 指令）
├── auth_clientarea.php              ← 前台权限点定义
├── config.php                       ← 插件配置表单定义（后台"插件配置"页自动渲染）
├── upgrade.php                      ← 版本升级钩子（本样例为空文件）
├── controller/
│   ├── TicketController.php         ← 后台控制器（放 controller 根目录）
│   └── clientarea/TicketController.php  ← 前台控制器（注意子目录 clientarea）
├── lang/{zh-cn,zh-hk,en-us}.php     ← PHP 语言包
├── logic/                           ← 业务逻辑层
├── model/                           ← 模型层（ThinkPHP 风格 Model + 静态方法）
├── validate/                        ← 校验器
├── upload/                          ← 附件目录
├── config/                          ← 子配置（如邮件模板 email_template/*.html）
└── template/
    ├── admin/
    │   ├── index.html               ← 后台入口页（对应 plugin/{插件名}/index.html）
    │   ├── ticket_setting.html      ← 二级页面（页面内跳转用 ticket_setting.htm）
    │   ├── api/*.js                 ← 每页的接口封装
    │   ├── js/*.js                  ← 每页的 Vue 实例
    │   ├── js/common/               ← vue.js / tdesign.min.js / axios.min.js / moment / layout
    │   ├── lang/{zh-cn,zh-hk,en-us}.js
    │   ├── css/*.css|less、img/、media/
    │   └── bak/*.php                ← 旧版 PHP 模板（已废弃，说明迁移历史）
    └── clientarea/
        ├── pc/{default,mf801,mf_pc_701}/    ← ★ 按"前台主题"分目录，每套一套页面
        └── mobile/{mfm201,mfm301}/          ← ★ 按"移动端模板"分目录
            └── ticket.html / js/ / css/ / api/ / img/ / lang/index.js
```

**要点**
- 静态资源通过**绝对路径**引用：`/plugins/addon/idcsmart_ticket/template/clientarea/pc/default/css/ticket.css`。
- 前台模板**按主题分目录**（`pc/default`、`pc/mf801`、`pc/mf_pc_701`、`mobile/mfm201`、`mobile/mfm301`）。⚠️ 主题缺目录时是否回落 default 需在实例上验证；保守做法是每套主题都放一份（用 iframe 方案时每份只有一个文件，成本极低）。
- 文件编码：**UTF-8，无 BOM**（已按字节验证 `lang/zh-cn.php` 中 `请求成功` = `e8 af b7 e6 b1 82 e6 88 90 e5 8a 9f`）。我们的代码同样用 UTF-8。

---

## 2. 主类 ✅

```php
namespace addon\idcsmart_ticket;

use app\common\lib\Plugin;

class IdcsmartTicket extends Plugin          // ★ 必须继承 app\common\lib\Plugin
{
    public $info = array(
        'name' => 'IdcsmartTicket', 'title' => '用户工单',
        'description' => '用户工单', 'author' => '智简魔方', 'version' => '4.2.2',
    );

    public function install()   { /* 直接跑 CREATE TABLE ... */ }
    public function uninstall() { /* 删表/删配置 */ }

    public function afterAdminDelete($param){ /* ★ 小驼峰方法即 hook 处理器 */ }
}
```

- 表名前缀约定：`idcsmart_addon_{目录名}_*`，如 `idcsmart_addon_idcsmart_ticket`、`idcsmart_addon_idcsmart_ticket_reply`。
- **本样例没有 `hooks.php`**，而是用主类小驼峰方法（`afterAdminDelete`）承接 hook。⚠️ 文档说 `hooks.php` + `add_hook()` 也可用；两种都支持时，功能越复杂越建议用 `hooks.php`（便于集中管理、不影响主类可读性）。

---

## 3. 控制器与基类 ✅（与文档不一致的一处）

```php
// 前台 controller/clientarea/TicketController.php
namespace addon\idcsmart_ticket\controller\clientarea;
use app\event\controller\PluginBaseController;      // ★ 真实命名空间
class TicketController extends PluginBaseController

// 后台 controller/TicketController.php（据 auth_clientarea.php 注释）
// ★ 后台基类：app\event\controller\PluginAdminBaseController
```

- **文档写的是 `app\admin\controller\PluginBaseController`，源码实际是 `app\event\controller\PluginBaseController`**。以源码为准。
- 可用辅助函数：`get_client_id()`、`get_admin_id()`、`request()->client_name`、`request()->admin_name`（`active_log()` 里大量使用）。
- 返回值约定：`json(['status'=>200,'msg'=>...,'data'=>...])` 或 `['status'=>200,'msg'=>...,'data'=>...]`（model 层直接返回数组，controller 再包 `json()`）。
- 校验：`$this->validate = new TicketValidate();`，失败时 `json(['status'=>400,'msg'=>lang_plugins($this->validate->getError())])`。
- 日志：`active_log(lang_plugins('key',['{client}'=>'client#'.$id.'#'.$name.'#']), 'addon_idcsmart_ticket', $ticketId)`。

---

## 4. 路由与中间件 ✅（与文档不一致的一处）

```php
// 前台：免登录组（★ 中间件是 Check，不是 CheckHome）
Route::group('console/v1/api', function () {
    Route::put('ticket/:id/status', "\\addon\\idcsmart_ticket\\controller\\clientarea\\TicketController@updateStatus")
        ->append(['_plugin'=>'idcsmart_ticket','_controller'=>'ticket','_action'=>'update_status']);
})->middleware(\app\http\middleware\ParamFilter::class)
  ->middleware(\app\http\middleware\Check::class);

// 前台：需登录
Route::group('console/v1', function () { ... })
  ->middleware(\app\http\middleware\ParamFilter::class)
  ->middleware(\app\http\middleware\CheckHome::class)
  ->middleware(\app\http\middleware\RejectRepeatRequest::class);   // ★ 防重复提交

// 后台：需管理员
Route::group(DIR_ADMIN . '/v1', function () { ... })
  ->middleware(\app\http\middleware\ParamFilter::class)
  ->middleware(\app\http\middleware\CheckAdmin::class);
```

| 中间件 | 作用 |
| --- | --- |
| `ParamFilter` | 参数过滤，三个组都挂了 |
| `Check` | **免登录**（不需要登录态，例如网关回调、开放接口） |
| `CheckHome` | 前台登录态 |
| `CheckAdmin` | 后台管理员登录态 |
| `RejectRepeatRequest` | 防重复请求（下单、提交类接口建议加） |

- 路由路径前缀是 `console/v1`（前台）与 `{DIR_ADMIN}/v1`（后台）。
- `->append([...])` 里带 `_plugin/_controller/_action` 后，可以用基类的通用方法；`_action` 为方法名的蛇形。

---

## 5. 前端机制 ✅（这一条彻底改变我们的前端方案）

**V10 的插件页面不是 PHP 模板引擎，而是「HTML 片段 + 独立 JS 创建 Vue 实例」**：

- 模板是 `.html` **片段**：开头直接跟 `<link>`，然后 `</head><body>...`，说明它被系统拼进完整的 HTML 布局里（含全局 `aside-menu`、`top-menu` 等组件）。
- 每个页面配一个 `js/*.js`，模式固定：

```js
(function (window) {
  window.onload = function () {
    const template = document.getElementsByClassName("template")[0];
    Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang);  // ★ i18n 注入
    new Vue({ components:{...}, data(){...}, created(){...}, updated(){...} }).$mount(template);
  };
})(window);
```

- 组件库：**前台用 Element UI（`el-*`），后台用 TDesign（`t-*`）**，均是全局引入的 Vue 2 运行时（`js/common/vue.js`）。后台公共库：`tdesign.min.js`、`axios.min.js`、`moment.min.js`、`layout.js`、`lang.js`。
- 语言：`window.lang`（系统语言）+ `window.plugin_lang`（插件语言，来自 `template/{admin,clientarea}/lang/index.js`），合到 `Vue.prototype.lang`。
- 后端接口封装在 `template/admin/api/*.js`，用 axios 请求 `/console/v1/...` 或 `/{DIR_ADMIN}/v1/...`。

**对我们的影响（重要结论）**
1. V10 前台是 **Vue 2 + Element UI**，后台是 **Vue 2 + TDesign**。我们的控制台是 **Vue 3**，**无法直接挂进 V10 页面**（除非上 Vue 2 或做微前端）。
2. 因此 **iframe 嵌入（方案 A）成为首选**：插件只出一个"壳页面"（`ticket.html` 同级的 `llm.html`），内容就是全屏 iframe 指向网关的 Vue3 控制台。每套主题目录里放一份同样的 10 行 HTML 即可。
3. 若日后要"融为一体"，做法是把 Vue3 打成 `iife` 包，用 `Vue3.createApp().mount('#czapi')` 挂在一个空 div 上（不依赖 V10 的 Vue2 实例），而不是复用 V10 的 Vue。这点在排期里作为 P7 可选项。

---

## 6. 导航与权限 ✅

`sidebar_clientarea.php`（前台导航，源码启用）：

```php
return [[
    'name'  => 'nav_plugin_addon_ticket_list',  // 语言键，需在 lang/ 下定义
    'url'   => 'ticket',                         // 自动补 .html
    'icon'  => '',
    'child' => [],
]];
```

`sidebar.php`（后台导航，源码被注释）：一级导航 `{name,url:'',icon,in:'',child:[{name,url,in,icon}]}`；默认后台导航指向 `plugin/{插件名}/index.html`。

`auth.php` / `auth_clientarea.php`：权限点 `{title(语言键), url(模板名), auth_rule:[命名空间::方法], description}`，前端用 `v-permission="'auth_user_ticket_configuration'"` 控制按钮。

---

## 7. 插件配置 ✅

`config.php` 返回配置表单定义，后台"插件配置"页自动渲染：

```php
return [
    'refresh_time' => [
        'title' => '刷新时间', 'type' => 'text',   // text/password/textarea/checkbox/radio/select
        'value' => 3, 'tip' => '',
    ],
];
```

→ 我们的 `gateway_base_url` / `app_id` / `app_secret`（type=password）/ `plan_map`（type=textarea）都放这里，**不需要自己写配置页**。

---

## 8. 对我们插件的落地清单

| 项 | 结论 |
| --- | --- |
| 目录 / 类名 | `idcsmart_llm_gateway/` + `IdcsmartLlmGateway.php`，`extends app\common\lib\Plugin` |
| 表名 | `idcsmart_addon_idcsmart_llm_gateway_key` / `_config` / `_sync_log` |
| 前台控制器 | `controller/clientarea/KeyController.php`，`extends app\event\controller\PluginBaseController` |
| 后台控制器 | `controller/KeyController.php`、`ChannelController.php`，`extends app\event\controller\PluginAdminBaseController` |
| 路由 | `console/v1`（`CheckHome`+`RejectRepeatRequest`）、`console/v1/api`（`Check`，给网关回调/免登录）、`{DIR_ADMIN}/v1`（`CheckAdmin`） |
| 免登录回调 | 网关 → V10 的"用量通知/额度同步"走 `console/v1/api/*` + `Check`，**但必须自己做 HMAC 校验**（`Check` 只代表不校验登录态，不代表安全） |
| 前台页面 | 每套主题一个极简 `llm.html`（iframe）；`template/clientarea/pc/{default,mf801,mf_pc_701}/`、`mobile/{mfm201,mfm301}/` |
| 前台导航 | `sidebar_clientarea.php` 加 `nav_plugin_addon_llm_gateway` |
| 后台页面 | `template/admin/index.html`（仪表盘/渠道）+ `key.html`（Key 封禁）+ `js/*.js` + `lang/*.js` |
| 配置 | `config.php`（网关地址、app_id/secret、套餐映射、默认计费模式） |
| 语言 | `lang/{zh-cn,zh-hk,en-us}.php`，键名带前缀 `llm_gw_`，避免与系统冲突 |
| 日志 | `active_log()` 记录 Key 创建/吊销/配置变更 |

---

## 9. 仍需在真实实例上验证的点（P0.5 最小穿透测试清单）

| # | 验证项 | 验收标准 |
| --- | --- | --- |
| V1 | 插件能被识别并安装 | 后台"插件"列表出现；`install()` 建表成功；`uninstall()` 干净 |
| V2 | 后台默认页可达 | `{站点}/{DIR_ADMIN}/plugin/idcsmart_llm_gateway/index.html` 正常渲染（含 TDesign 样式） |
| V3 | 前台路由免登录 + 登录态 | `console/v1/api/ping`（`Check`）免登录 200；`console/v1/llm/keys`（`CheckHome`）未登录 401、登录 200 |
| V4 | 前台导航注入 | `sidebar_clientarea.php` 的菜单出现在会员中心侧栏，点击进入 iframe 页 |
| V5 | 一个 hook 生效 | 用 `after_order_create` 或 `afterAdminDelete` 打日志，确认触发参数结构 |
| V6 | 主题兼容 | 在 3 套 PC 主题 + 1 套移动模板下页面均可用 |
| V7 | 插件配置读写 | `config.php` 表单保存后，代码里能读到（以及加密存储 secret 的做法） |
| V8 | API 鉴权/JWT | 前台 axios 带 JWT 调插件接口成功；跨站/过期表现符合预期 |

> V1–V5 是"最小限度测试"，预计 1.5–2 人日。V6–V8 放到插件 MVP 阶段。
