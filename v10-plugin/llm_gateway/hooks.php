<?php
/**
 * CZAPI —— LLM 网关模块 Hook 注册
 *
 * 依据 plan 附录 A：「模块路径 public/plugins/server/{模块名}，主类同名；hooks.php 在模块加载即引入，
 * 需自行判断适用范围」。因此本文件里的每个回调都必须先判断"这个 hook 是不是本模块的产品"，
 * 否则会误处理其它模块（如其它 server 模块）的产品事件。
 *
 * ⚠️ 待实例验证：V10 模块 hooks.php 的加载时机与 add_hook() 的可用性来自「模块开发」文档描述，
 *    本仓库无实例可验证；若实例未引入本文件，模块主类里的小驼峰方法（如 afterCreateFirstServer）
 *    仍然可用。Hook 事件名（get_host_flow 等）同样需在实例的 Hook 列表中确认。
 *
 * @author  CZAPI
 * @version 1.0.0
 */

use server\llm_gateway\LlmGatewayModule;

/* ---------------------------------------------------------------------------
 * 按量计费取数：V10 的自动任务按周期调用，取某台产品在该期段内产生的消费金额。
 *
 * 参数（plan 附录 A 已核实）：host(HostModel)、module、start_time、end_time
 * 返回：交给 V10 出账的用量数组，主类 getHostFlow() 已做兼容封装。
 * ------------------------------------------------------------------------- */
add_hook('get_host_flow', function ($param) {
    $moduleName = '';

    if (is_array($param)) {
        $moduleName = (string) ($param['module'] ?? '');
    } elseif (is_object($param)) {
        $moduleName = (string) ($param->module ?? '');
    }

    /* 只处理本模块的产品：其它模块的取数请求原样放行（返回空数组让 V10 继续走别的模块） */
    if ($moduleName !== '' && $moduleName !== 'llm_gateway' && $moduleName !== 'LlmGatewayModule') {
        return [];
    }

    if (!class_exists(LlmGatewayModule::class)) {
        return [];
    }

    $module = new LlmGatewayModule();

    return $module->getHostFlow(is_array($param) ? $param : (array) $param);
});
