<?php
namespace server\llm_gateway;

use think\facade\Db;

/**
 * CZAPI —— V10 商品模块（把 LLM 套餐做成可在 V10 售卖的产品）
 *
 * 目录约定：public/plugins/server/{模块名}/，主类同名（plan 附录 A「已核实的 V10 关键事实」）。
 * V10 在加载模块时会自动 include 同目录的 hooks.php，模块需自行判断适用范围。
 *
 * ============================================================================
 * 与插件的关系（plan §4.2 职责边界，避免端上重复实现）
 *   - 模块 = 商品生命周期（开通/暂停/恢复/删除/续费），由 V10 订单驱动
 *   - 插件 = 用户自助面（Key 管理/用量/卡密）+ 后台运营面
 *   两者共用同一个网关客户端（插件里的 model/LlmApi.php），不在模块里重写签名逻辑。
 * ============================================================================
 *
 * @author  CZAPI
 * @version 1.0.0
 */
class LlmGatewayModule
{
    # 默认表前缀：模块用 idcsmart_module_{模块}_{具体}（与插件的 addon_ 前缀区分）
    const TABLE_PREFIX = 'idcsmart_module_llm_gateway';

    /**
     * 模块基本信息（系统读取）
     *
     * // ⚠️ 待实例验证: V10 模块的 metaData() 字段集合来自「模块开发」文档（适用 V10.5.6），
     * //   本仓库无实例、无官方模块源码可对照。这里只返回最保守的三个键
     * //   （display_name / version / description），即使实例需要更多键也不会因缺键报错。
     *
     * @return array
     */
    public function metaData()
    {
        return [
            'display_name' => 'LLM 网关套餐',
            'version'      => '1.0.0',
            'description'  => '在 V10 内售卖 CZAPI 的 LLM API 套餐：开通即建订阅、暂停即封 Key',
        ];
    }

    /**
     * 连通性测试（后台「接口管理」状态灯）
     *
     * 必须真实探测：调用网关 GET /internal/v1/health。
     *
     * @param array $param 模块配置（app_id/app_secret/gateway_base_url 等）
     * @return array ['status'=>200|400, 'msg'=>string, 'data'=>array]
     */
    public function testConnect($param = [])
    {
        require_once $this->apiClientPath();
        $apiClass = $this->apiClientClass();

        if (!class_exists($apiClass)) {
            return $this->fail('未找到网关客户端 LlmApi，请确认 idcsmart_llm_gateway 插件已安装');
        }

        $res = $apiClass::health();

        if ((int) $res['code'] !== 0) {
            return $this->fail(
                '网关不可达：' . ($res['msg'] !== '' ? $res['msg'] : '未知错误')
                . '（HTTP ' . (int) ($res['http_status'] ?? 0) . '）'
            );
        }

        return [
            'status' => 200,
            'msg'    => '连接成功',
            'data'   => is_array($res['data']) ? $res['data'] : [],
        ];
    }

    /**
     * 首次创建该模块的产品时触发：建模块自己的表（套餐档位与商品配置对照）
     *
     * 幂等：用 IF NOT EXISTS，重复触发无副作用。
     *
     * // ⚠️ 待实例验证: 该方法名与调用时机来自 plan §9 与模块开发文档，
     * //   但"是否在创建商品时真正被调用"需在实例上确认；因此建表逻辑在 createAccount 里也兜底执行一次。
     *
     * @return bool
     */
    public function afterCreateFirstServer()
    {
        return $this->ensureSchema();
    }

    /**
     * 开通 / 自动开通
     *
     * 流程（plan §9 + §5.3）：
     *   1. 从 $param 里取 host / client / product / server
     *   2. 读商品自定义字段：plan_code / quota_tokens / billing_mode / model_group / rpm / tpm / concurrency
     *   3. 调 /internal/v1/accounts/sync 建账户映射
     *   4. 调 /internal/v1/subscriptions 建订阅（幂等键 v10_host_id）
     *   5. 本地记录 host -> subscription 映射，供暂停/删除时使用
     *
     * **失败必须返回 status=400 且给出可执行的中文原因**，绝不允许为了"看起来成功"返回 200
     * （plan §9 末段明确要求：V10 会让订单停在"开通失败"，msg 是客服唯一的排查线索）。
     *
     * @param array $param ['host'=>[], 'client'=>[], 'product'=>[], 'server'=>[]]
     * @return array ['status'=>int, 'msg'=>string, 'data'=>array]
     */
    public function createAccount($param)
    {
        $ctx = $this->extractContext($param);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        require_once $this->apiClientPath();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        # 1) 商品配置 -> 网关套餐
        $productId = (int) $ctx['product']['id'];
        $planCode = $this->customField($ctx['product'], 'plan_code');
        if ($planCode === '') {
            # 允许后台用插件配置的 plan_map 兜底（商品ID -> plan_code）
            $planCode = $this->planCodeFromMap($productId);
        }
        if ($planCode === '') {
            return $this->fail(
                '商品未配置套餐（plan_code）。请在商品配置里添加自定义字段 plan_code（basic/pro/max），'
                . '或在插件配置的「套餐映射」里把商品ID ' . $productId . ' 映射到某个 plan_code'
            );
        }

        $billingMode = $this->customField($ctx['product'], 'billing_mode');
        if ($billingMode === '') {
            $billingMode = $this->pluginConfig('default_billing_mode', 'hybrid');
        }
        if (!in_array($billingMode, ['subscription', 'balance', 'hybrid'], true)) {
            return $this->fail('商品配置 billing_mode 非法：' . $billingMode . '（只能是 subscription / balance / hybrid）');
        }

        $quotaTokens = (int) $this->customField($ctx['product'], 'quota_tokens');
        $modelGroup = $this->customField($ctx['product'], 'model_group');

        $limits = [];
        foreach (['rpm' => 'rpm', 'tpm' => 'tpm', 'concurrency' => 'concurrency'] as $field => $key) {
            $value = (int) $this->customField($ctx['product'], $field);
            if ($value > 0) {
                $limits[$key] = $value;
            }
        }

        # 2) 账户同步（upsert，天然幂等）
        $syncRes = $apiClass::syncAccount((int) $ctx['client_id'], $ctx['email'], 1);
        if ((int) $syncRes['code'] !== 0) {
            return $this->fail(
                '创建网关账户失败：' . $this->errMsg($syncRes)
                . '。请检查插件配置的网关地址 / app_id / app_secret 是否正确'
            );
        }

        # 3) 建订阅（幂等键 v10_host_id；重复调用返回 created=false）
        $period = $this->resolvePeriod($param, $ctx);
        $payload = [
            'v10_client_id' => (int) $ctx['client_id'],
            'v10_host_id'   => (int) $ctx['host']['id'],
            'plan_code'     => $planCode,
            'billing_mode'  => $billingMode,
            'period_start'  => $period['start'],
            'period_end'    => $period['end'],
        ];
        if ($quotaTokens > 0) {
            $payload['quota_tokens'] = $quotaTokens;
        }
        if ($modelGroup !== '') {
            $payload['model_group'] = $modelGroup;
        }
        if (!empty($limits)) {
            $payload['limits'] = $limits;
        }

        $subRes = $apiClass::createSubscription($payload);
        if ((int) $subRes['code'] !== 0) {
            return $this->fail('创建网关订阅失败：' . $this->errMsg($subRes));
        }

        $subscriptionId = (int) ($subRes['data']['subscription_id'] ?? 0);
        if ($subscriptionId <= 0) {
            return $this->fail('网关已响应但未返回 subscription_id，请检查网关版本是否支持 /internal/v1/subscriptions');
        }

        # 4) 本地映射（失败不回滚订阅：订阅可随时由 host_id 重新查到，映射只是加速）
        try {
            $this->ensureSchema();
            $this->saveHostSubscription(
                (int) $ctx['host']['id'],
                (int) $ctx['client_id'],
                $subscriptionId,
                $planCode,
                $billingMode,
                $period['end']
            );
        } catch (\Throwable $e) {
            # 映射写入失败不影响开通结果，但必须留痕
            $this->log('createAccount 本地映射写入失败：' . $e->getMessage());
        }

        return [
            'status' => 200,
            'msg'    => '开通成功',
            'data'   => [
                'subscription_id' => $subscriptionId,
                'plan_code'       => $planCode,
                'billing_mode'    => $billingMode,
                'period_end'      => $period['end'],
                'created'         => (bool) ($subRes['data']['created'] ?? true),
            ],
        ];
    }

    /**
     * 暂停（到期 / 超用 / 欠费 / 实名未完成 / 下游）
     *
     * 必须尊重 V10 传来的 suspend_type 与 suspend_reason（plan 附录 A）：
     *   suspend_type ∈ overdue | overtraffic | certification_not_complete | other | downstream
     * 这些值原样透传给网关，网关据此决定恢复时的行为与提示文案。
     *
     * @param array $param ['host'=>[], 'suspend_type'=>string, 'suspend_reason'=>string]
     * @return array
     */
    public function suspendAccount($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        require_once $this->apiClientPath();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        $hostId = (int) $ctx['host']['id'];
        $clientId = (int) $ctx['client_id'];

        $suspendType = (string) ($param['suspend_type'] ?? 'other');
        $allowed = ['overdue', 'overtraffic', 'certification_not_complete', 'other', 'downstream'];
        if (!in_array($suspendType, $allowed, true)) {
            $suspendType = 'other';
        }
        $suspendReason = (string) ($param['suspend_reason'] ?? $suspendType);

        $subscriptionId = $this->resolveSubscriptionId($hostId, $clientId, $apiClass);
        if ($subscriptionId <= 0) {
            return $this->fail('未找到产品 #' . $hostId . ' 对应的网关订阅，暂停失败（请先用插件后台刷新该客户的 Key 镜像）');
        }

        $res = $apiClass::suspendSubscription($subscriptionId, $suspendReason, $suspendType);
        if ((int) $res['code'] !== 0) {
            return $this->fail('网关暂停订阅失败：' . $this->errMsg($res));
        }

        try {
            $this->updateLocalStatus($hostId, 'suspended');
        } catch (\Throwable $e) {
            $this->log('suspendAccount 本地状态更新失败：' . $e->getMessage());
        }

        return [
            'status' => 200,
            'msg'    => '暂停成功',
            'data'   => [
                'subscription_id' => $subscriptionId,
                'suspend_type'    => $suspendType,
                'suspend_reason'  => $suspendReason,
            ],
        ];
    }

    /**
     * 恢复
     *
     * @param array $param ['host'=>[]]
     * @return array
     */
    public function unsuspendAccount($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        require_once $this->apiClientPath();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        $hostId = (int) $ctx['host']['id'];
        $subscriptionId = $this->resolveSubscriptionId($hostId, (int) $ctx['client_id'], $apiClass);
        if ($subscriptionId <= 0) {
            return $this->fail('未找到产品 #' . $hostId . ' 对应的网关订阅，恢复失败');
        }

        $res = $apiClass::resumeSubscription($subscriptionId);
        if ((int) $res['code'] !== 0) {
            return $this->fail('网关恢复订阅失败：' . $this->errMsg($res));
        }

        try {
            $this->updateLocalStatus($hostId, 'active');
        } catch (\Throwable $e) {
            $this->log('unsuspendAccount 本地状态更新失败：' . $e->getMessage());
        }

        return [
            'status' => 200,
            'msg'    => '恢复成功',
            'data'   => ['subscription_id' => $subscriptionId],
        ];
    }

    /**
     * 删除产品：终止订阅（网关侧吊销该订阅下全部 Key）
     *
     * 幂等：DELETE /internal/v1/subscriptions/{id} 重复调用不会报错（契约 §1.3）。
     * 因此即使 V10 重试删除，也不会造成二次扣费或残留 Key。
     *
     * @param array $param ['host'=>[]]
     * @return array
     */
    public function terminateAccount($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        require_once $this->apiClientPath();
        if (!class_exists($apiClass)) {
            # 插件被卸载时也要能删产品：此时不做远程吊销，只清本地映射，返回成功避免卡住删除流程
            try {
                $this->updateLocalStatus((int) $ctx['host']['id'], 'terminated');
            } catch (\Throwable $e) {
                # 忽略
            }

            return ['status' => 200, 'msg' => '插件未安装，已跳过远程吊销', 'data' => []];
        }

        $hostId = (int) $ctx['host']['id'];
        $subscriptionId = $this->resolveSubscriptionId($hostId, (int) $ctx['client_id'], $apiClass);

        if ($subscriptionId > 0) {
            $res = $apiClass::terminateSubscription($subscriptionId);
            # 404 = 已经不存在，视为成功（幂等）
            if ((int) $res['code'] !== 0 && (int) $res['code'] !== 40400) {
                return $this->fail('网关终止订阅失败：' . $this->errMsg($res));
            }
        }

        try {
            $this->updateLocalStatus($hostId, 'terminated');
        } catch (\Throwable $e) {
            $this->log('terminateAccount 本地状态更新失败：' . $e->getMessage());
        }

        return [
            'status' => 200,
            'msg'    => '删除成功',
            'data'   => ['subscription_id' => $subscriptionId],
        ];
    }

    /**
     * 续费：延长周期 + 重置周期额度
     *
     * 幂等键用 v10_host_id + 目标 period_end（契约 §1.3：POST /subscriptions/{id}/renew 用 period_end 幂等）。
     * 因此同一目标周期重复调用不会重复延长。
     *
     * @param array $param ['host'=>[], 'custom'=>[], 'duration'=>int, 'unit'=>string]
     * @return array
     */
    public function renew($param)
    {
        $ctx = $this->extractContext($param, false);
        if ($ctx['error'] !== '') {
            return $this->fail($ctx['error']);
        }

        $apiClass = $this->apiClientClass();
        require_once $this->apiClientPath();
        if (!class_exists($apiClass)) {
            return $this->fail('网关客户端缺失：请先安装并启用 idcsmart_llm_gateway 插件');
        }

        $hostId = (int) $ctx['host']['id'];
        $subscriptionId = $this->resolveSubscriptionId($hostId, (int) $ctx['client_id'], $apiClass);
        if ($subscriptionId <= 0) {
            return $this->fail('未找到产品 #' . $hostId . ' 对应的网关订阅，续费失败');
        }

        $period = $this->resolvePeriod($param, $ctx);

        $subRes = $apiClass::getSubscription($subscriptionId);
        if ((int) $subRes['code'] !== 0) {
            return $this->fail('读取网关订阅失败：' . $this->errMsg($subRes));
        }

        $planCode = (string) ($subRes['data']['plan_code'] ?? '');
        if ($planCode === '') {
            $planCode = $this->customField($ctx['product'], 'plan_code');
        }
        if ($planCode === '') {
            return $this->fail('无法确定套餐（网关订阅未返回 plan_code，商品也未配置），请人工核查');
        }

        $payload = [
            'v10_client_id' => (int) $ctx['client_id'],
            'v10_host_id'   => $hostId,
            'plan_code'     => $planCode,
            'period_start'  => $period['start'],
            'period_end'    => $period['end'],
            'renew'         => true,
        ];

        $quotaTokens = (int) $this->customField($ctx['product'], 'quota_tokens');
        if ($quotaTokens > 0) {
            $payload['quota_tokens'] = $quotaTokens;
        }

        $res = $apiClass::renewSubscription($payload);
        if ((int) $res['code'] !== 0) {
            return $this->fail('网关续费失败：' . $this->errMsg($res));
        }

        try {
            $this->updateLocalPeriod($hostId, $period['end']);
        } catch (\Throwable $e) {
            $this->log('renew 本地周期更新失败：' . $e->getMessage());
        }

        return [
            'status' => 200,
            'msg'    => '续费成功',
            'data'   => [
                'subscription_id' => $subscriptionId,
                'period_end'      => $period['end'],
                'applied'         => (bool) ($res['data']['applied'] ?? true),
            ],
        ];
    }

    /**
     * 按量计费取数（hook: get_host_flow）
     *
     * 契约用 GET /internal/v1/billing/host-usage；返回值需让 V10 能算出"本期用了多少钱"。
     *
     * ⚠️ 返回值结构来自 plan §9 与定时任务 Hook 文档的描述，**字段名未在实例上验证**；
     * 这里同时给出多种常见命名（total/used/amount/cost），并在 README 待验证清单里标注。
     *
     * @param array $param ['host'=>HostModel|array, 'module'=>string, 'start_time'=>int, 'end_time'=>int]
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

        $apiClass = $this->apiClientClass();
        require_once $this->apiClientPath();
        if (!class_exists($apiClass)) {
            # 插件缺失时返回 0 用量，避免卡住 V10 的对账任务
            return ['status' => 200, 'msg' => 'ok', 'data' => ['total' => 0, 'amount' => 0, 'cost_micros' => 0]];
        }

        $from = (int) ($param['start_time'] ?? 0);
        $to = (int) ($param['end_time'] ?? time());

        $res = $apiClass::hostUsage($hostId, $from, $to);
        if ((int) $res['code'] !== 0) {
            # 取数失败不能返回"用量 0"，否则会静默漏计费；返回 400 让 V10 记录异常
            return $this->fail('读取网关用量失败：' . $this->errMsg($res));
        }

        $data = is_array($res['data']) ? $res['data'] : [];
        $costMicros = (int) ($data['cost_micros'] ?? 0);

        return [
            'status' => 200,
            'msg'    => 'ok',
            'data'   => [
                'total'             => $costMicros,
                'amount'            => $costMicros,
                'cost_micros'       => $costMicros,
                'requests'          => (int) ($data['requests'] ?? 0),
                'prompt_tokens'     => (int) ($data['prompt_tokens'] ?? 0),
                'completion_tokens' => (int) ($data['completion_tokens'] ?? 0),
                'from'              => $from,
                'to'                => $to,
            ],
        ];
    }

    /* ==================================================================
     * 内部辅助
     * ================================================================== */

    /**
     * 网关客户端类名（复用插件里的 LlmApi，模块不重复实现签名）
     *
     * @return string
     */
    private function apiClientClass()
    {
        return 'addon\\idcsmart_llm_gateway\\model\\LlmApi';
    }

    /**
     * 网关客户端文件路径
     *
     * @return string
     */
    private function apiClientPath()
    {
        return dirname(__DIR__, 2) . '/addon/idcsmart_llm_gateway/model/LlmApi.php';
    }

    /**
     * 读插件配置（与插件共用一份配置，避免模块/插件两处各配一套密钥）
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
     * 从插件套餐映射里取 plan_code
     *
     * @param int $productId
     * @return string
     */
    private function planCodeFromMap($productId)
    {
        $logicClass = 'addon\\idcsmart_llm_gateway\\logic\\LlmGatewayLogic';
        if (!class_exists($logicClass)) {
            return '';
        }

        return (string) $logicClass::planCodeForProduct($productId);
    }

    /**
     * 归一化 V10 传入的上下文
     *
     * V10 各版本传的键名略有差异（host / host_id；client / client_id），这里全部兼容。
     *
     * // ⚠️ 待实例验证: createAccount($param) 中 $param 的确切键结构未在实例上验证，
     * //   以下兼容映射覆盖了 plan §9 描述的 host/client/product/server 四段结构。
     *
     * @param array $param
     * @param bool  $needClient 是否必须拿到客户信息
     * @return array ['host'=>array,'client'=>array,'product'=>array,'client_id'=>int,'email'=>string,'error'=>string]
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

        # 产品信息可能只给了 product_id，补查一次
        if (empty($product['id']) && !empty($host['product_id'])) {
            try {
                $product = Db::name('product')->where('id', (int) $host['product_id'])->find() ?: [];
            } catch (\Throwable $e) {
                $product = [];
            }
        }

        if ($needClient && $clientId <= 0) {
            return ['error' => '缺少客户信息（client.id），无法开通'];
        }

        $email = (string) ($client['email'] ?? '');
        if ($email === '' && $clientId > 0) {
            # 票据与账户同步都要 email；取不到时用稳定占位值，保证流程不断（网关侧仅用于展示）
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
     * V10 商品的配置项可能出现在 custom 字段里，命名有多种风格（plan_code / planCode / 自定义字段数组），
     * 这里按"扁平键优先 -> custom_field 容器 -> custom 容器"的顺序渐退查找。
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
                # 有些实例的自定义字段是 [{name, value}] 形式
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
     * 计算计费周期
     *
     * 优先用 V10 传的 duration/unit（如 1 个月）；取不到时按"从现在起 1 个月"生成，
     * 并在返回里标注 estimated，便于排查"周期为什么不对"。
     *
     * // ⚠️ 待实例验证: V10 续费/开通时传入的时长字段名（duration/unit 或 custom 数组）未在实例上验证；
     * //   当前对 duration/unit、custom['duration']、host['duration'] 做了兼容。
     *
     * @param array $param
     * @param array $ctx
     * @return array ['start'=>int,'end'=>int,'estimated'=>bool]
     */
    private function resolvePeriod($param, array $ctx)
    {
        $start = (int) ($ctx['host']['active_time'] ?? 0);
        if ($start <= 0) {
            $start = time();
        }

        $duration = (int) ($param['duration'] ?? ($param['custom']['duration'] ?? ($ctx['host']['duration'] ?? 0)));
        $unit = (string) ($param['unit'] ?? ($param['custom']['unit'] ?? ($ctx['host']['unit'] ?? 'month')));
        $estimated = false;

        if ($duration <= 0) {
            $duration = 1;
            $unit = 'month';
            $estimated = true;
        }

        $end = $this->addPeriod($start, $duration, $unit);

        return ['start' => $start, 'end' => $end, 'estimated' => $estimated];
    }

    /**
     * 时间加法（月/年按自然日推进，日/小时按秒推进）
     *
     * @param int    $ts
     * @param int    $duration
     * @param string $unit day|month|year|hour
     * @return int
     */
    private function addPeriod($ts, $duration, $unit)
    {
        $duration = max(1, (int) $duration);

        switch (strtolower((string) $unit)) {
            case 'hour':
            case 'hours':
                return $ts + 3600 * $duration;
            case 'day':
            case 'days':
                return $ts + 86400 * $duration;
            case 'year':
            case 'years':
                return strtotime('+' . $duration . ' year', $ts) ?: ($ts + 86400 * 365 * $duration);
            case 'month':
            case 'months':
            default:
                return strtotime('+' . $duration . ' month', $ts) ?: ($ts + 86400 * 30 * $duration);
        }
    }

    /**
     * 解析 host 对应的网关订阅 ID
     *
     * 先查本地映射表，查不到再走网关 /accounts/{id}/quota 的 subscriptions 列表比对 v10_host_id。
     *
     * @param int    $hostId
     * @param int    $clientId
     * @param string $apiClass
     * @return int
     */
    private function resolveSubscriptionId($hostId, $clientId, $apiClass)
    {
        try {
            $this->ensureSchema();
            $row = Db::name(self::TABLE_PREFIX . '_host_subscription')
                ->where('host_id', (int) $hostId)
                ->find();
            if (!empty($row) && (int) $row['subscription_id'] > 0) {
                return (int) $row['subscription_id'];
            }
        } catch (\Throwable $e) {
            # 表不存在等情况，继续走远程查询
        }

        if ($clientId <= 0) {
            return 0;
        }

        $subscriptionId = (int) $apiClass::findSubscriptionIdByHost($clientId, $hostId);
        if ($subscriptionId > 0) {
            try {
                $this->saveHostSubscription($hostId, $clientId, $subscriptionId, '', '', 0);
            } catch (\Throwable $e) {
                # 映射写入失败不影响返回
            }
        }

        return $subscriptionId;
    }

    /**
     * 建模块表（host <-> subscription 映射）
     *
     * // ⚠️ 待实例验证: 运行期直接 CREATE TABLE IF NOT EXISTS 是权宜之计
     * //   （模块没有像插件那样的 install() 时机）。若实例禁止运行期 DDL，
     * //   需要在 afterCreateFirstServer()（若确实会被调用）或后台维护脚本里预建。
     *
     * @return bool
     */
    private function ensureSchema()
    {
        Db::execute(
            "CREATE TABLE IF NOT EXISTS `" . self::TABLE_PREFIX . "_host_subscription` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `host_id` int(11) NOT NULL DEFAULT '0' COMMENT 'V10产品ID',
  `client_id` int(11) NOT NULL DEFAULT '0' COMMENT 'V10客户ID',
  `subscription_id` int(11) NOT NULL DEFAULT '0' COMMENT '网关订阅ID',
  `plan_code` varchar(64) NOT NULL DEFAULT '' COMMENT '套餐编码',
  `billing_mode` varchar(16) NOT NULL DEFAULT '' COMMENT 'subscription|balance|hybrid',
  `status` varchar(16) NOT NULL DEFAULT 'active' COMMENT 'active|suspended|terminated',
  `period_end` int(11) NOT NULL DEFAULT '0' COMMENT '周期结束时间',
  `create_time` int(11) NOT NULL DEFAULT '0' COMMENT '创建时间',
  `update_time` int(11) NOT NULL DEFAULT '0' COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_host_id` (`host_id`),
  KEY `idx_subscription_id` (`subscription_id`) USING BTREE,
  KEY `idx_client_id` (`client_id`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC COMMENT='LLM网关模块-产品订阅映射';"
        );

        return true;
    }

    /**
     * 写/更新 host -> subscription 映射
     *
     * @return void
     */
    private function saveHostSubscription($hostId, $clientId, $subscriptionId, $planCode, $billingMode, $periodEnd)
    {
        $now = time();
        $data = [
            'client_id'       => (int) $clientId,
            'subscription_id' => (int) $subscriptionId,
            'plan_code'       => (string) $planCode,
            'billing_mode'    => (string) $billingMode,
            'status'          => 'active',
            'update_time'     => $now,
        ];
        if ((int) $periodEnd > 0) {
            $data['period_end'] = (int) $periodEnd;
        }

        $exist = Db::name(self::TABLE_PREFIX . '_host_subscription')->where('host_id', (int) $hostId)->find();
        if (empty($exist)) {
            Db::name(self::TABLE_PREFIX . '_host_subscription')->insert(array_merge($data, [
                'host_id'     => (int) $hostId,
                'create_time' => $now,
            ]));
        } else {
            Db::name(self::TABLE_PREFIX . '_host_subscription')->where('host_id', (int) $hostId)->update($data);
        }
    }

    /**
     * 更新本地状态
     *
     * @param int    $hostId
     * @param string $status
     * @return void
     */
    private function updateLocalStatus($hostId, $status)
    {
        $this->ensureSchema();
        Db::name(self::TABLE_PREFIX . '_host_subscription')
            ->where('host_id', (int) $hostId)
            ->update(['status' => (string) $status, 'update_time' => time()]);
    }

    /**
     * 更新本地周期结束时间
     *
     * @param int $hostId
     * @param int $periodEnd
     * @return void
     */
    private function updateLocalPeriod($hostId, $periodEnd)
    {
        $this->ensureSchema();
        Db::name(self::TABLE_PREFIX . '_host_subscription')
            ->where('host_id', (int) $hostId)
            ->update(['period_end' => (int) $periodEnd, 'update_time' => time()]);
    }

    /**
     * 统一失败返回（status=400 + 可执行的中文原因）
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
     * 写模块日志（用 V10 的 active_log，便于在后台日志里检索）
     *
     * @param string $msg
     * @return void
     */
    private function log($msg)
    {
        try {
            active_log('[LLM网关模块] ' . $msg, 'llm_gateway_module', 0);
        } catch (\Throwable $e) {
            # 日志失败不影响主流程
        }
    }
}
