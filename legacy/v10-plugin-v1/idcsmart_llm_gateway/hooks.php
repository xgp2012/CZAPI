<?php
/**
 * Hook 注册
 *
 * 官方 idcsmart_ticket 4.2.2 **没有 hooks.php**，而是用主类小驼峰方法（afterAdminDelete）承接 hook
 * （docs/v10-notes.md §2 的 ⚠️ 说明）；模块开发文档则明确「钩子由 hooks.php 实现」。
 * 本项目事件较多，统一放在这里便于集中管理。
 *
 * ── 事件名的来源分级（重要，决定上线前的核对强度）──────────────────────────
 * 【A 级·已在官方 V10 开发文档「Hook 开发」逐页核实】
 *   five_minute_cron  参数 config, output
 *   daily_cron        参数 config, output          ← 注意不是 day_cron
 *   after_host_delete 模块开发文档给出的 add_hook 示例事件
 * 【B 级·plan §8.3 / 文档示例提到，但本次未能逐页核实】
 *   order_paid        plan 附录 A 的「订单相关钩子示例」里列出
 *
 * // ⚠️ 待实例验证: order_paid 事件名。本次逐一核对了开发文档「公共业务 Hook / 订单、支付与退款」
 * //   的 21 个条目（before_order_pay、auto_direct_pay_order、after_order_refund、after_order_cancel 等），
 * //   其中没有直接的"订单支付后"条目，说明 order_paid 可能属"通知与消息"分类或与文档版本不一致。
 * //   上线前必须在目标实例的后台 Hook 列表里确认实际事件名；若不存在，把本文件里的 'order_paid'
 * //   换成实例上的真实事件名即可（回调函数体不用改）。
 * //   兜底手段：万一该 hook 从未触发，充值不会自动加额度——此时可在插件后台「推送队列」页
 * //   手动补投，或临时改用 next 一段注释里的 before_order_pay + 订单状态二次校验方案。
 *
 * @author  CZAPI
 * @version 1.0.0
 */

use addon\idcsmart_llm_gateway\model\LlmApi;

/*
 * 支付成功：充值/调账类订单 -> 网关加额度
 *
 * 产品订单（带 host_id）会被 onOrderPaid() 自动跳过，交给模块的 createAccount / renew 处理，
 * 避免"套餐额度"和"余额"被加两次。
 *
 * 若实例的 order_paid 回调参数结构不同（常见为 ['id'=>int] 或 ['order_id'=>int] 或直接 int），
 * 下面的取值逻辑已做兼容。
 */
add_hook('order_paid', function ($param) {
    $orderId = 0;

    if (is_array($param)) {
        $orderId = (int) ($param['id'] ?? ($param['order_id'] ?? 0));
    } elseif (is_object($param)) {
        $orderId = (int) ($param->id ?? ($param->order_id ?? 0));
    } elseif (is_numeric($param)) {
        $orderId = (int) $param;
    }

    if ($orderId <= 0) {
        return false;
    }

    LlmApi::onOrderPaid($orderId);

    return true;
});

/*
 * 产品删除：终止订阅并吊销 Key
 *
 * 与模块的 terminateAccount() 是两条路径：
 *   - terminateAccount: V10 按"正常删除产品"流程调用模块
 *   - after_host_delete: 兜底，防止模块未被调用（例如后台强制删除、批量清理）
 * 两者都幂等（网关 §2.15 终止是幂等的），重复调用无副作用。
 */
add_hook('after_host_delete', function ($param) {
    LlmApi::onHostDeleted($param);

    return true;
});

/*
 * 每天：失败推送重投 + 用量快照
 *
 * 参数：config(系统配置)、output(命令行输出对象)。
 * 这里把进度写到 output，方便运维从计划任务日志里看到"今天补投了几条"。
 */
add_hook('daily_cron', function ($param) {
    $report = LlmApi::dailySync();

    if (is_array($param) && isset($param['output']) && is_object($param['output'])) {
        $param['output']->writeln(
            '[LLM网关] 每日任务：补投扫描 ' . (int) ($report['retry']['scanned'] ?? 0)
            . ' 条，成功 ' . (int) ($report['retry']['done'] ?? 0)
            . ' 条，失败 ' . (int) ($report['retry']['failed'] ?? 0)
            . ' 条；用量快照 ' . (int) ($report['usage_keys'] ?? 0) . ' 条'
        );
    }

    return true;
});

/*
 * 每五分钟：失败推送重投
 *
 * 比 daily_cron 及时得多：客户投诉"充了没到账"时最多等 5 分钟就能自动补齐。
 * 参数：config、output（与 daily_cron 相同）。
 */
add_hook('five_minute_cron', function ($param) {
    $report = LlmApi::retryFailed(50);

    if (is_array($param) && isset($param['output']) && is_object($param['output'])) {
        if ((int) $report['scanned'] > 0) {
            $param['output']->writeln(
                '[LLM网关] 5 分钟任务：补投扫描 ' . (int) $report['scanned']
                . ' 条，成功 ' . (int) $report['done']
                . ' 条，失败 ' . (int) $report['failed'] . ' 条'
            );
        }
    }

    return true;
});
