<?php
namespace server\llm_gateway;

use think\facade\Db;

/**
 * V10 商品模块（把 LLM 套餐做成可在 V10 售卖的产品）
 *
 * 目录约定：public/plugins/server/{模块名}/，主类同名。
 * V10 在加载模块时会自动 include 同目录的 hooks.php。
 *
 * ============================================================================
 * 职责边界（关键设计）
 *   - 模块 = 商品生命周期（开通/暂停/恢复/删除/续费），由 V10 订单驱动
 *   - 插件 = 用户自助面（Key 管理/用量/卡密）+ 后台运营面
 *   两者共用插件里的 model/LlmApi.php，签名逻辑只有一份。
 *
 * 与旧版的差异：网关内部改为 host 语义后，模块不再维护
 * "host -> subscription_id" 本地映射表 —— 网关侧 v10_hosts 表就是事实来源，
 * 所有操作直接用 host_id，少一张表就少一处不一致。
 * ============================================================================
 *
 * @author  CZAPI
 * @version 2.0.0
 */
class LlmGatewayModule
{
    /**
     * 模块基本信息
     *
     * @return array
     */
    public function metaData()
    {
        return [
            'display_name' => 'LLM 网关套餐',
            'version'      => '2.0.0',
            'description'  => '在 V10 内售卖 LLM API 套餐：开通即绑定 host、暂停即封 Key、续费原地延长',
        ];
    }

    /**
     * 连通性测试（后台「接口管理」状态灯）
     *
     * @param array $param
     * @return array
     */
    public function testConnect($param = [])
    {
        $apiClass = $this->apiClientClass();
        $this->requireApiClient();

        if (!class_exists($apiClass)) {
            return $this->fail('未找到网关客户端 LlmApi，请确认 idcsmart_llm_gateway 插件已安装');
        }

        $res = $apiClass::health();
        if ((int) $res['code'] !== 0) {
            return $this->fail(
                '网关不可达：' . $this->errMsg($res)
            );
        }

        return [
            'status' => 200,
            'msg'    => '连接成功',
            'data'   => is_array($res['data']) ? $res['data'] : [],
        ];
    }

    /**
     * 开通 / 自动开通
     *
     * 流程：
     *   1. 归一化上下文，取 host / client / product
     *   2. 商品配置 -> 网关套餐 ID（plan_id）
     *   3. 调 /accounts/sync 确保账户存在（upsert，天然幂等）
     *   4. 调 /hosts 绑定 host 到套餐（同一 host 重复调用是续期，不是新开）
     *
     * 失败必须返回 status=400 且给出可执行的中文原因。V10 会让订单停在
     * "开通失败"，msg 是客服唯一的排查线索，绝不允许为了"看起来成功"返回 200。
     *
     * @param array $param
     * @return array ['status'=>int, 'msg'=>string, 'data'=>array]
     */
    public function createAccount($param)
    {
        $ctx = $this->extractContext($param);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        $this->requireApiClient();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        $productId = (int) $ctx['product']['id'];
        $planId = $this->resolvePlanId($ctx['product'], $productId);
        if ($planId <= 0) {
            return $this->fail(
                '商品未配置网关套餐。请在商品自定义字段里填 plan_id（网关套餐的数字 ID，可在网关后台「订阅套餐」页查看），'
                . '或在插件配置的「套餐映射」里把商品ID ' . $productId . ' 映射到某个套餐ID'
            );
        }

        $syncRes = $apiClass::syncAccount((int) $ctx['client_id'], (string) ($ctx['client']['name'] ?? ''), $ctx['email']);
        if ((int) $syncRes['code'] !== 0) {
            return $this->fail(
                '创建网关账户失败：' . $this->errMsg($syncRes)
                . '。请检查插件配置的网关地址 / app_id / app_secret 是否正确'
            );
        }

        $durationDays = $this->resolveDurationDays($param, $ctx);

        # ref_type/ref_id 是幂等凭证：同一订单重复触发只会绑定一次。
        $refId = (string) ($param['order_id'] ?? ($ctx['host']['order_id'] ?? ($ctx['host']['id'])));
        $bindRes = $apiClass::bindHost([
            'client_id'     => (int) $ctx['client_id'],
            'host_id'       => (int) $ctx['host']['id'],
            'plan_id'       => $planId,
            'duration_days' => $durationDays,
            'ref_type'      => 'v10_host_open',
            'ref_id'        => $refId,
        ]);
        if ((int) $bindRes['code'] !== 0) {
            return $this->fail('绑定网关套餐失败：' . $this->errMsg($bindRes));
        }

        $data = is_array($bindRes['data']) ? $bindRes['data'] : [];

        $this->log(
            '开通 host#' . $ctx['host']['id'] . ' 客户#' . $ctx['client_id'] . ' 套餐#' . $planId
        );

        return [
            'status' => 200,
            'msg'    => '开通成功',
            'data'   => [
                'host_id'          => (int) ($data['host_id'] ?? $ctx['host']['id']),
                'plan_id'          => $planId,
                'plan_title'       => (string) ($data['plan_title'] ?? ''),
                'host_status'      => (string) ($data['host_status'] ?? 'active'),
                'quota_total'      => (int) ($data['quota_total'] ?? 0),
                'quota_remaining'  => (int) ($data['quota_remaining'] ?? 0),
                'end_time'         => (int) ($data['end_time'] ?? 0),
            ],
        ];
    }

    /**
     * 暂停（到期 / 超用 / 欠费 / 实名未完成 / 下游）
     *
     * 网关侧暂停会同时禁用该 host 下全部 Key，避免"欠费还能用"。
     *
     * @param array $param
     * @return array
     */
    public function suspendAccount($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        $this->requireApiClient();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        $suspendType = (string) ($param['suspend_type'] ?? 'other');
        $allowed = ['overdue', 'overtraffic', 'certification_not_complete', 'other', 'downstream'];
        if (!in_array($suspendType, $allowed, true)) {
            $suspendType = 'other';
        }
        $reason = (string) ($param['suspend_reason'] ?? $suspendType);

        $res = $apiClass::setHostStatus((int) $ctx['host']['id'], 'suspended', $reason);
        if ((int) $res['code'] !== 0) {
            return $this->fail('网关暂停失败：' . $this->errMsg($res));
        }

        $this->log('暂停 host#' . $ctx['host']['id'] . ' 原因=' . $reason . ' 类型=' . $suspendType);

        return [
            'status' => 200,
            'msg'    => '暂停成功',
            'data'   => [
                'host_id'        => (int) $ctx['host']['id'],
                'suspend_type'   => $suspendType,
                'suspend_reason' => $reason,
            ],
        ];
    }

    /**
     * 恢复
     *
     * @param array $param
     * @return array
     */
    public function unsuspendAccount($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        $this->requireApiClient();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        $res = $apiClass::setHostStatus((int) $ctx['host']['id'], 'active', 'V10 恢复');
        if ((int) $res['code'] !== 0) {
            return $this->fail('网关恢复失败：' . $this->errMsg($res));
        }

        $this->log('恢复 host#' . $ctx['host']['id']);

        return [
            'status' => 200,
            'msg'    => '恢复成功',
            'data'   => ['host_id' => (int) $ctx['host']['id']],
        ];
    }

    /**
     * 删除产品：终止（网关侧吊销该 host 下全部 Key）
     *
     * 幂等：host 已终止时网关直接返回成功，因此 V10 重试删除不会报错。
     *
     * @param array $param
     * @return array
     */
    public function terminateAccount($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        $this->requireApiClient();
        if (!class_exists($apiClass)) {
            # 插件被卸载时也要能删产品：不做远程吊销，返回成功避免卡住删除流程。
            return ['status' => 200, 'msg' => '插件未安装，已跳过远程吊销', 'data' => []];
        }

        $hostId = (int) $ctx['host']['id'];
        $res = $apiClass::setHostStatus($hostId, 'terminated', 'V10 产品删除');

        # 404 = host 从未绑定过，视为已终止（幂等）。
        if ((int) $res['code'] !== 0 && (int) $res['code'] !== $apiClass::CODE_NOT_FOUND) {
            return $this->fail('网关终止失败：' . $this->errMsg($res));
        }

        $this->log('终止 host#' . $hostId);

        return [
            'status' => 200,
            'msg'    => '删除成功',
            'data'   => ['host_id' => $hostId],
        ];
    }

    /**
     * 续费：延长周期
     *
     * 网关侧 /hosts 对已绑定的 host 是"原地续期"，不会新开订阅，
     * 所以这里复用同一个调用，只是幂等凭证换成续费订单号。
     *
     * @param array $param
     * @return array
     */
    public function renew($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        $this->requireApiClient();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        $planId = $this->resolvePlanId($ctx['product'], (int) $ctx['product']['id']);
        if ($planId <= 0) {
            return $this->fail('无法确定套餐：商品未配置 plan_id，续费失败，请人工核查');
        }

        $durationDays = $this->resolveDurationDays($param, $ctx);

        # 续费的幂等凭证必须**在重试之间保持不变**：V10 的续费请求超时后会重发，
        # 而超时的那一次往往已经成功。凭证若每次都不同，网关就会当成一次新的续费
        # 再顺延一个周期，白送客户时间。
        #
        # 优先用订单号。取不到就退到 V10 自己的到期日 due_time：它在同一次续费的
        # 多次重试之间不变，而下一次续费时必然已经前移，因此不会挡住正常的续费。
        #
        # 不能用 time()（每次重试都变），也不能用网关侧的 end_time——首次成功之后
        # 它就变了，重试会拿到新凭证，等于没去重。
        #
        # 两者都拿不到时**直接报错**，而不是编一个稳定假凭证：假凭证的后果是
        # 客户第二次续费被静默挡掉（付了钱没拿到时间），比报错更难查。
        $refId = (string) ($param['order_id'] ?? '');
        if ($refId === '') {
            $dueTime = (int) ($ctx['host']['due_time'] ?? 0);
            if ($dueTime <= 0) {
                return $this->fail('无法确定续费幂等凭证：hook 未提供 order_id，host 也没有 due_time，续费中止以免重复计期');
            }
            $refId = 'RENEW-' . (int) $ctx['host']['id'] . '-' . $dueTime;
        }

        $res = $apiClass::bindHost([
            'client_id'     => (int) $ctx['client_id'],
            'host_id'       => (int) $ctx['host']['id'],
            'plan_id'       => $planId,
            'duration_days' => $durationDays,
            'ref_type'      => 'v10_host_renew',
            'ref_id'        => $refId,
        ]);
        if ((int) $res['code'] !== 0) {
            return $this->fail('网关续费失败：' . $this->errMsg($res));
        }

        $data = is_array($res['data']) ? $res['data'] : [];
        $this->log('续费 host#' . $ctx['host']['id'] . ' 到 ' . (int) ($data['end_time'] ?? 0));

        return [
            'status' => 200,
            'msg'    => '续费成功',
            'data'   => [
                'host_id'  => (int) $ctx['host']['id'],
                'end_time' => (int) ($data['end_time'] ?? 0),
            ],
        ];
    }

    /**
     * 按量计费取数（hook: get_host_flow）
     *
     * 返回本期花费，供 V10 算出"这个产品这个月用了多少钱"。
     *
     * @param array $param
     * @return array
     */
    public function getHostFlow($param)
    {
        $host = $param['host'] ?? [];
        if (is_object($host)) {
            $host = method_exists($host, 'toArray') ? $host->toArray() : (array) $host;
        }
        $hostId = (int) ($host['id'] ?? ($param['host_id'] ?? 0));
        if ($hostId <= 0) {
            return ['status' => 400, 'msg' => '缺少 host'];
        }

        $clientId = (int) ($host['client_id'] ?? ($param['client_id'] ?? 0));
        if ($clientId <= 0) {
            return ['status' => 400, 'msg' => '缺少 client_id，无法取用量'];
        }

        $apiClass = $this->apiClientClass();
        $this->requireApiClient();
        if (!class_exists($apiClass)) {
            # 插件缺失时返回 0，避免卡住 V10 的对账任务。
            return ['status' => 200, 'msg' => 'ok', 'data' => ['total' => 0, 'amount' => 0, 'cost_micros' => 0]];
        }

        $from = (int) ($param['start_time'] ?? 0);
        $to = (int) ($param['end_time'] ?? time());

        $res = $apiClass::usageSummary([
            'client_id' => $clientId,
            'from'      => $from,
            'to'        => $to,
            'group_by'  => 'day',
        ]);
        if ((int) $res['code'] !== 0) {
            # 取数失败不能返回"用量 0"，否则会静默漏计费。
            return $this->fail('读取网关用量失败：' . $this->errMsg($res));
        }

        $totals = is_array($res['data']['totals'] ?? null) ? $res['data']['totals'] : [];
        $costMicros = (int) ($totals['quota_micros'] ?? 0);

        return [
            'status' => 200,
            'msg'    => 'ok',
            'data'   => [
                'total'             => $costMicros,
                'amount'            => $costMicros,
                'cost_micros'       => $costMicros,
                'requests'          => (int) ($totals['request_count'] ?? 0),
                'prompt_tokens'     => (int) ($totals['prompt_tokens'] ?? 0),
                'completion_tokens' => (int) ($totals['completion_tokens'] ?? 0),
                'from'              => $from,
                'to'                => $to,
            ],
        ];
    }

    /* ==================================================================
     * 内部辅助
     * ================================================================== */

    private function apiClientClass()
    {
        return 'addon\\idcsmart_llm_gateway\\model\\LlmApi';
    }

    private function apiClientPath()
    {
        return dirname(__DIR__, 2) . '/addon/idcsmart_llm_gateway/model/LlmApi.php';
    }

    /**
     * 确保插件客户端类可用（模块与插件共用同一份签名实现）
     *
     * @return void
     */
    private function requireApiClient()
    {
        if (class_exists($this->apiClientClass())) {
            return;
        }
        $path = $this->apiClientPath();
        if (is_file($path)) {
            require_once $path;
        }
    }

    /**
     * 读插件配置
     *
     * @param string $key
     * @param mixed  $default
     * @return mixed
     */
    private function pluginConfig($key, $default = null)
    {
        $logicClass = 'addon\\idcsmart_llm_gateway\\logic\\LlmGatewayLogic';
        if (!class_exists($logicClass)) {
            $logicPath = dirname(__DIR__, 2) . '/addon/idcsmart_llm_gateway/logic/LlmGatewayLogic.php';
            if (is_file($logicPath)) {
                require_once $logicPath;
            }
        }

        if (class_exists($logicClass)) {
            return $logicClass::config($key, $default);
        }

        return $default;
    }

    /**
     * 解析商品的网关套餐 ID
     *
     * 顺序：商品自定义字段 plan_id -> 插件套餐映射 -> 0（调用方报错）
     *
     * @param array $product
     * @param int   $productId
     * @return int
     */
    private function resolvePlanId(array $product, $productId)
    {
        $planId = (int) $this->customField($product, 'plan_id');
        if ($planId > 0) {
            return $planId;
        }

        $logicClass = 'addon\\idcsmart_llm_gateway\\logic\\LlmGatewayLogic';
        $this->requireApiClient();
        if (!class_exists($logicClass)) {
            require_once dirname(__DIR__, 2) . '/addon/idcsmart_llm_gateway/logic/LlmGatewayLogic.php';
        }
        if (class_exists($logicClass)) {
            return $logicClass::planIdForProduct($productId);
        }

        return 0;
    }

    /**
     * 归一化 V10 传入的上下文
     *
     * V10 各版本传的键名略有差异（host / host_id；client / client_id），这里全部兼容。
     *
     * @param array $param
     * @param bool  $needClient
     * @return array
     */
    private function extractContext($param, $needClient = true)
    {
        $host = $param['host'] ?? [];
        if (is_object($host)) {
            $host = method_exists($host, 'toArray') ? $host->toArray() : (array) $host;
        }
        $client = $param['client'] ?? [];
        if (is_object($client)) {
            $client = method_exists($client, 'toArray') ? $client->toArray() : (array) $client;
        }
        $product = $param['product'] ?? [];
        if (is_object($product)) {
            $product = method_exists($product, 'toArray') ? $product->toArray() : (array) $product;
        }

        $hostId = (int) ($host['id'] ?? ($param['host_id'] ?? 0));
        if ($hostId <= 0) {
            return ['error' => '缺少产品信息（host.id），无法处理'];
        }
        $host['id'] = $hostId;

        $clientId = (int) ($client['id'] ?? ($param['client_id'] ?? ($host['client_id'] ?? 0)));

        # 产品信息可能只给了 product_id，补查一次。
        if (empty($product['id']) && !empty($host['product_id'])) {
            try {
                $product = Db::name('product')->where('id', (int) $host['product_id'])->find() ?: [];
            } catch (\Throwable $e) {
                $product = [];
            }
        }

        if ($needClient && $clientId <= 0) {
            return ['error' => '缺少客户信息（client.id），无法处理'];
        }

        $email = (string) ($client['email'] ?? '');
        if ($email === '' && $clientId > 0) {
            # 账户同步要 email；取不到时用稳定占位值，保证流程不断（网关侧仅用于展示）。
            $email = 'client' . $clientId . '@v10.local';
        }

        return [
            'host'      => $host,
            'client'    => $client,
            'product'   => is_array($product) ? $product : [],
            'client_id' => $clientId,
            'email'     => $email,
            'error'     => '',
        ];
    }

    /**
     * 读商品自定义字段
     *
     * @param array  $product
     * @param string $key
     * @return string
     */
    private function customField(array $product, $key)
    {
        if (isset($product[$key]) && !is_array($product[$key])) {
            return trim((string) $product[$key]);
        }

        foreach (['custom', 'custom_field', 'config', 'options', 'self_defined_field'] as $container) {
            if (!empty($product[$container]) && is_array($product[$container])) {
                if (isset($product[$container][$key]) && !is_array($product[$container][$key])) {
                    return trim((string) $product[$container][$key]);
                }
                # 有些实例的自定义字段是 [{name, value}] 形式。
                foreach ($product[$container] as $item) {
                    if (is_array($item) && (($item['name'] ?? '') === $key)) {
                        return trim((string) ($item['value'] ?? ''));
                    }
                }
            }
        }

        return '';
    }

    /**
     * 把 V10 传的时长换算成天数
     *
     * 网关按天数延长订阅，所以月/年要折算。用自然月 30 天、自然年 365 天，
     * 与 V10 前台展示的"1 个月"口径一致；精确到日的场景由 V10 直接传天。
     *
     * @param array $param
     * @param array $ctx
     * @return int
     */
    private function resolveDurationDays($param, array $ctx)
    {
        $duration = (int) ($param['duration'] ?? ($param['custom']['duration'] ?? ($ctx['host']['duration'] ?? 0)));
        $unit = strtolower((string) ($param['unit'] ?? ($param['custom']['unit'] ?? ($ctx['host']['unit'] ?? 'month'))));

        if ($duration <= 0) {
            $duration = 1;
            $unit = 'month';
        }

        return match ($unit) {
            'hour', 'hours' => max(1, (int) ceil($duration / 24)),
            'day', 'days' => $duration,
            'week', 'weeks' => $duration * 7,
            'year', 'years' => $duration * 365,
            default => $duration * 30,
        };
    }

    /**
     * 统一失败返回
     *
     * @param string $msg
     * @return array
     */
    private function fail($msg)
    {
        return ['status' => 400, 'msg' => (string) $msg, 'data' => []];
    }

    /**
     * 从网关响应里取可读错误信息
     *
     * @param array $res
     * @return string
     */
    private function errMsg(array $res)
    {
        $msg = (string) ($res['msg'] ?? '');
        if ($msg === '') {
            $msg = '未知错误';
        }

        return $msg . '（code=' . (int) ($res['code'] ?? 0) . '，HTTP ' . (int) ($res['http_status'] ?? 0) . '）';
    }

    /**
     * 写模块日志
     *
     * @param string $msg
     * @return void
     */
    private function log($msg)
    {
        try {
            active_log('[LLM网关模块] ' . $msg, 'llm_gateway', 0);
        } catch (\Throwable $e) {
            # 日志失败不影响主流程。
        }
    }
}
