<?php
namespace addon\idcsmart_llm_gateway\model;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use think\facade\Db;

/**
 * CZAPI 网关内部接口客户端（插件 -> 网关，服务端到服务端）
 *
 * ============================================================================
 * 事实来源：docs/internal-api-v1.md（冻结版）。任何字段名、路径、信封结构都必须与该文一致。
 *   - 统一前缀 /internal/v1
 *   - 响应信封 {"code":0,"msg":"ok","data":{...}}
 *   - 请求头 X-CZ-App-Id / X-CZ-Timestamp / X-CZ-Nonce / X-CZ-Signature / Content-Type
 *   - 待签串 = METHOD\nPATH\nSORTED_QUERY\nSHA256_HEX(BODY)\nTIMESTAMP\nNONCE（无尾随换行）
 *   - 签名   = hex(HMAC_SHA256(app_secret, 待签串))
 * ============================================================================
 *
 * 本类同时承担 plan §10.3 的"可靠投递"：
 *   先写 _pushlog(pending) -> 立即尝试 -> 按 HTTP/业务码分类 -> done / failed(指数退避)
 *
 * @author  CZAPI
 * @version 1.0.0
 */
class LlmApi
{
    # ------------------------------------------------------------------
    # 业务码（docs/internal-api-v1.md §1.4）
    # ------------------------------------------------------------------
    const CODE_OK                 = 0;
    const CODE_PARAM_ERROR        = 40000;
    const CODE_MISSING_IDEMPOTENT = 40001;
    const CODE_SIGN_INVALID       = 40100;
    const CODE_TIMESTAMP_INVALID  = 40101;
    const CODE_NONCE_REPLAY       = 40102;
    const CODE_APP_DISABLED       = 40300;
    const CODE_NOT_FOUND          = 40400;
    const CODE_IDEMPOTENT_CLASH   = 40900;
    const CODE_INSUFFICIENT       = 40200;
    const CODE_RATE_LIMITED       = 42900;
    const CODE_INTERNAL           = 50000;
    const CODE_FEATURE_DISABLED   = 50300;

    # 传输层失败（不是网关返回的业务码，由本类自行定义，避免与 §1.4 混淆）
    const CODE_NETWORK            = -1;   // 连接失败 / 超时
    const CODE_BAD_RESPONSE       = -2;   // 非 JSON / 信封缺字段

    # 内部接口前缀
    const PREFIX = '/internal/v1';

    # ==================================================================
    # 一、签名与请求
    # ==================================================================

    /**
     * 计算签名（与 Go 侧逐字节一致）
     *
     * 严格照 docs/internal-api-v1.md §3：
     *   $raw = implode("\n", [strtoupper($method), $path, $sortedQuery,
     *                         hash('sha256', $body), (string)$ts, $nonce]);
     *   return hash_hmac('sha256', $raw, $secret);
     *
     * @param string       $method HTTP 方法
     * @param string       $path   含前缀的路径，如 /internal/v1/keys
     * @param array|string $query  查询参数数组（内部会 canonicalQuery）或已排序好的查询串
     * @param string       $body   请求体原始字节
     * @param int          $ts     unix 秒
     * @param string       $nonce  32 位十六进制
     * @param string|null  $secret 留空则取配置 app_secret
     * @return string hex 小写
     */
    public static function sign($method, $path, $query, $body, $ts, $nonce, $secret = null)
    {
        if ($secret === null) {
            $secret = (string) LlmGatewayLogic::config('app_secret', '');
        }

        $sortedQuery = is_array($query) ? self::canonicalQuery($query) : (string) $query;

        $raw = implode("\n", [
            strtoupper((string) $method),
            (string) $path,
            $sortedQuery,
            hash('sha256', (string) $body),
            (string) $ts,
            (string) $nonce,
        ]);

        return hash_hmac('sha256', $raw, (string) $secret);
    }

    /**
     * 查询串规范化：key 按 ASCII 升序，值用 rawurlencode
     *
     * 注意：Go 侧用 url.QueryEscape（空格 -> '+'），PHP 用 rawurlencode（空格 -> %20）。
     * 目前所有 /internal/v1 查询串都不含空格，因此两者等价（见契约 §3 的注）。
     *
     * @param array $q
     * @return string
     */
    public static function canonicalQuery(array $q)
    {
        ksort($q, SORT_STRING);
        $parts = [];
        foreach ($q as $k => $v) {
            $parts[] = rawurlencode((string) $k) . '=' . rawurlencode((string) $v);
        }

        return implode('&', $parts);
    }

    /**
     * 发起一次内部接口请求
     *
     * @param string $method  GET|POST|PATCH|DELETE
     * @param string $path    含 /internal/v1 前缀的路径
     * @param array  $payload 请求体（空数组 = 无请求体，此时 body 为空串）
     * @param array  $query   查询参数
     * @return array [
     *   'code'        => int,    // 0 成功；否则为 §1.4 业务码或本类的 -1/-2
     *   'msg'         => string,
     *   'data'        => mixed,
     *   'http_status' => int,    // 0 表示未拿到 HTTP 响应（网络层失败）
     *   'raw'         => string, // 原始响应体，排障用
     * ]
     */
    public static function request($method, $path, array $payload = [], array $query = [])
    {
        $method = strtoupper((string) $method);

        $baseUrl = rtrim((string) LlmGatewayLogic::config('gateway_base_url', ''), '/');
        $appId = (string) LlmGatewayLogic::config('app_id', '');
        $secret = (string) LlmGatewayLogic::config('app_secret', '');

        if ($baseUrl === '') {
            return self::fail(self::CODE_PARAM_ERROR, '插件未配置网关地址 gateway_base_url', 0);
        }
        if ($appId === '' || $secret === '') {
            return self::fail(self::CODE_PARAM_ERROR, '插件未配置 app_id / app_secret', 0);
        }
        if (!function_exists('curl_init')) {
            return self::fail(self::CODE_NETWORK, 'PHP 未启用 cURL 扩展，无法调用网关', 0);
        }

        # 请求体：空数组 = 空体（sha256("") 参与签名）
        $body = empty($payload)
            ? ''
            : json_encode($payload, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);

        $sortedQuery = self::canonicalQuery($query);
        $url = $baseUrl . $path . ($sortedQuery !== '' ? '?' . $sortedQuery : '');

        $ts = time();
        $nonce = bin2hex(random_bytes(16));
        $signature = self::sign($method, $path, $sortedQuery, $body, $ts, $nonce, $secret);

        $headers = [
            'X-CZ-App-Id: ' . $appId,
            'X-CZ-Timestamp: ' . $ts,
            'X-CZ-Nonce: ' . $nonce,
            'X-CZ-Signature: ' . $signature,
            'Content-Type: application/json; charset=utf-8',
            'Accept: application/json',
        ];

        $timeoutMs = LlmGatewayLogic::configInt('timeout_ms', 5000);
        if ($timeoutMs <= 0) {
            $timeoutMs = 5000;
        }

        $ch = curl_init();
        curl_setopt($ch, CURLOPT_URL, $url);
        curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
        curl_setopt($ch, CURLOPT_HTTPHEADER, $headers);
        curl_setopt($ch, CURLOPT_CONNECTTIMEOUT_MS, min($timeoutMs, 3000));
        curl_setopt($ch, CURLOPT_TIMEOUT_MS, $timeoutMs);
        curl_setopt($ch, CURLOPT_FOLLOWLOCATION, false);
        curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, true);
        curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, 2);
        curl_setopt($ch, CURLOPT_CUSTOMREQUEST, $method);
        curl_setopt($ch, CURLOPT_HEADER, true);

        if ($body !== '') {
            curl_setopt($ch, CURLOPT_POSTFIELDS, $body);
        }

        # 响应签名校验（可选，plan §8.7）：默认关闭，防止网关未实现时误判
        $responseHeaders = [];
        if ((string) LlmGatewayLogic::config('verify_response_sign', '0') === '1') {
            curl_setopt($ch, CURLOPT_HEADERFUNCTION, function ($curl, $line) use (&$responseHeaders) {
                $pos = strpos($line, ':');
                if ($pos !== false) {
                    $name = strtolower(trim(substr($line, 0, $pos)));
                    $responseHeaders[$name] = trim(substr($line, $pos + 1));
                }

                return strlen($line);
            });
        }

        $raw = curl_exec($ch);

        if ($raw === false) {
            $errNo = curl_errno($ch);
            $err = curl_error($ch);
            curl_close($ch);

            return self::fail(self::CODE_NETWORK, 'cURL 错误(' . $errNo . ')：' . $err, 0);
        }

        $httpStatus = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
        $headerSize = (int) curl_getinfo($ch, CURLINFO_HEADER_SIZE);
        curl_close($ch);

        # 因为开了 CURLOPT_HEADER，需要按头长度切掉响应头
        $responseBody = substr($raw, $headerSize);

        # 可选：校验响应签名
        if ($responseHeaders) {
            $ok = self::verifyResponse($responseBody, $responseHeaders, $secret);
            if (!$ok) {
                return self::fail(self::CODE_BAD_RESPONSE, '响应签名校验失败（疑似被中间人篡改）', $httpStatus, $responseBody);
            }
        }

        $decoded = json_decode($responseBody, true);
        if (!is_array($decoded) || !array_key_exists('code', $decoded)) {
            return self::fail(
                self::CODE_BAD_RESPONSE,
                '网关响应不是合法信封（HTTP ' . $httpStatus . '）',
                $httpStatus,
                $responseBody
            );
        }

        return [
            'code'        => (int) $decoded['code'],
            'msg'         => (string) ($decoded['msg'] ?? ''),
            'data'        => $decoded['data'] ?? null,
            'http_status' => $httpStatus,
            'raw'         => $responseBody,
        ];
    }

    /**
     * 校验网关响应签名（可选）
     *
     * X-CZ-Sign-Server = hex(HMAC(secret, sha256hex(body) + timestamp))
     *
     * @param string $body
     * @param array  $headers 小写键名的响应头
     * @param string $secret
     * @return bool
     */
    private static function verifyResponse($body, array $headers, $secret)
    {
        $sign = $headers['x-cz-sign-server'] ?? '';
        $ts = $headers['x-cz-timestamp-server'] ?? '';
        if ($sign === '' || $ts === '') {
            return false;
        }

        $expected = hash_hmac('sha256', hash('sha256', (string) $body) . (string) $ts, (string) $secret);

        return hash_equals($expected, strtolower((string) $sign));
    }

    /**
     * 统一构造失败信封
     *
     * @param int    $code
     * @param string $msg
     * @param int    $httpStatus
     * @param string $raw
     * @return array
     */
    private static function fail($code, $msg, $httpStatus = 0, $raw = '')
    {
        return [
            'code'        => (int) $code,
            'msg'         => (string) $msg,
            'data'        => null,
            'http_status' => (int) $httpStatus,
            'raw'         => (string) $raw,
        ];
    }

    /**
     * 按 §1.4 对一次请求结果分类（供可靠投递使用）
     *
     * 返回值：
     *   'ok'    -> 成功，标记 done
     *   'done'  -> 4xx 且重投无意义，标记 done 并记录差异
     *   'retry' -> 429 / 5xx / 超时 / 网络错误 / 40101 校时，进指数退避
     *   'stop'  -> 503 功能开关关闭，停止重试并告警
     *
     * @param array $res request() 的返回
     * @return string
     */
    public static function classify(array $res)
    {
        $code = (int) ($res['code'] ?? self::CODE_INTERNAL);
        $http = (int) ($res['http_status'] ?? 0);

        # 网络层失败：无 HTTP 响应
        if ($code === self::CODE_NETWORK) {
            return 'retry';
        }

        # 成功
        if ($code === self::CODE_OK && ($http === 0 || $http < 400)) {
            return 'ok';
        }

        # nonce 重放 = 网关已处理过，按完成处理（§1.4）
        if ($code === self::CODE_NONCE_REPLAY) {
            return 'done';
        }

        # 功能开关关闭：停止重试并告警（§1.4）
        if ($code === self::CODE_FEATURE_DISABLED) {
            return 'stop';
        }

        # 时间戳超窗：按文档要求"校时后重试一次"
        if ($code === self::CODE_TIMESTAMP_INVALID) {
            return 'retry';
        }

        # 限流：退避重试
        if ($code === self::CODE_RATE_LIMITED) {
            return 'retry';
        }

        # 5xx / 网关内部错误：进重试队列
        if ($http >= 500 || $code === self::CODE_INTERNAL) {
            return 'retry';
        }

        # 其余（400/40000、40001、40100、409 等）：不重试，记录差异
        return 'done';
    }

    # ==================================================================
    # 二、类型化封装 —— docs/internal-api-v1.md §2 全量
    # ==================================================================

    /**
     * §2.1 GET /internal/v1/health —— 健康探测（模块 testConnect 用）
     *
     * @return array
     */
    public static function health()
    {
        return self::request('GET', self::PREFIX . '/health');
    }

    /**
     * §2.2 POST /internal/v1/accounts/sync —— 创建或更新账户
     *
     * @param int    $v10ClientId
     * @param string $email
     * @param int    $status 1 正常
     * @return array
     */
    public static function syncAccount($v10ClientId, $email, $status = 1)
    {
        return self::request('POST', self::PREFIX . '/accounts/sync', [
            'v10_client_id' => (int) $v10ClientId,
            'email'         => (string) $email,
            'status'        => (int) $status,
        ]);
    }

    /**
     * §2.3 POST /internal/v1/credit —— 加/扣余额额度（原样调用，不做重试编排）
     *
     * 需要"不丢 + 幂等 + 重试"的场景请用 creditAccount()。
     *
     * @param array $payload 见契约 §2.3；**ref_type / ref_id 必填**
     * @return array
     */
    public static function credit(array $payload)
    {
        # 契约 §1.3/§2.3：缺少幂等凭证是编码错误（400/40001），本地先拦下
        if (empty($payload['ref_type']) || empty($payload['ref_id'])) {
            return self::fail(self::CODE_MISSING_IDEMPOTENT, '缺少幂等凭证 ref_type / ref_id，拒绝发送', 0);
        }

        return self::request('POST', self::PREFIX . '/credit', $payload);
    }

    /**
     * §2.4 POST /internal/v1/keys —— 创建 Key（明文只返回这一次）
     *
     * @param array $payload v10_client_id / name / models / ip_whitelist / expires_at / subscription_id / request_id
     * @return array
     */
    public static function createKey(array $payload)
    {
        return self::request('POST', self::PREFIX . '/keys', $payload);
    }

    /**
     * §2.5 GET /internal/v1/keys —— Key 列表
     *
     * @param int      $v10ClientId
     * @param int|null $subscriptionId
     * @return array
     */
    public static function listKeys($v10ClientId, $subscriptionId = null)
    {
        $query = ['v10_client_id' => (int) $v10ClientId];
        if ($subscriptionId !== null && (int) $subscriptionId > 0) {
            $query['subscription_id'] = (int) $subscriptionId;
        }

        return self::request('GET', self::PREFIX . '/keys', [], $query);
    }

    /**
     * §2.6 PATCH /internal/v1/keys/{id} —— 部分更新
     *
     * 只允许 name / status / models / ip_whitelist / expires_at（契约明列），其余键本地丢弃。
     *
     * @param int   $keyId
     * @param array $fields
     * @return array
     */
    public static function updateKey($keyId, array $fields)
    {
        $allowed = ['name', 'status', 'models', 'ip_whitelist', 'expires_at'];
        $payload = [];
        foreach ($allowed as $key) {
            if (array_key_exists($key, $fields)) {
                $payload[$key] = $fields[$key];
            }
        }

        if (empty($payload)) {
            return self::fail(self::CODE_PARAM_ERROR, '没有可更新的字段', 0);
        }

        return self::request('PATCH', self::PREFIX . '/keys/' . (int) $keyId, $payload);
    }

    /**
     * §2.7 DELETE /internal/v1/keys/{id} —— 吊销（幂等）
     *
     * @param int $keyId
     * @return array
     */
    public static function deleteKey($keyId)
    {
        return self::request('DELETE', self::PREFIX . '/keys/' . (int) $keyId);
    }

    /**
     * §2.8 GET /internal/v1/accounts/{client_id}/quota —— 套餐 + 余额两桶
     *
     * @param int $v10ClientId
     * @return array
     */
    public static function quota($v10ClientId)
    {
        return self::request(
            'GET',
            self::PREFIX . '/accounts/' . (int) $v10ClientId . '/quota'
        );
    }

    /**
     * §2.9 GET /internal/v1/models —— 可售模型目录
     *
     * @return array
     */
    public static function models()
    {
        return self::request('GET', self::PREFIX . '/models');
    }

    /**
     * §2.10 GET /internal/v1/usage/summary —— 用量汇总
     *
     * @param array $params v10_client_id(必填) / v10_host_id / api_key_id / from / to / group_by
     * @return array
     */
    public static function usageSummary(array $params)
    {
        if (empty($params['v10_client_id'])) {
            return self::fail(self::CODE_PARAM_ERROR, 'usageSummary 缺少 v10_client_id', 0);
        }

        $query = [];
        foreach (['v10_client_id', 'v10_host_id', 'api_key_id', 'from', 'to', 'group_by'] as $key) {
            if (isset($params[$key]) && $params[$key] !== '' && $params[$key] !== null) {
                $query[$key] = $params[$key];
            }
        }

        return self::request('GET', self::PREFIX . '/usage/summary', [], $query);
    }

    /**
     * §2.11 GET /internal/v1/usage/logs —— 明细分页（不含 prompt 正文）
     *
     * @param array $params v10_client_id(必填) / api_key_id / model / from / to / page / page_size
     * @return array
     */
    public static function usageLogs(array $params)
    {
        if (empty($params['v10_client_id'])) {
            return self::fail(self::CODE_PARAM_ERROR, 'usageLogs 缺少 v10_client_id', 0);
        }

        $query = [];
        foreach (['v10_client_id', 'api_key_id', 'model', 'from', 'to', 'page', 'page_size'] as $key) {
            if (isset($params[$key]) && $params[$key] !== '' && $params[$key] !== null) {
                $query[$key] = $params[$key];
            }
        }
        # 契约：page_size <= 200
        if (isset($query['page_size'])) {
            $pageSize = (int) $query['page_size'];
            $query['page_size'] = $pageSize > 200 ? 200 : max(1, $pageSize);
        }

        return self::request('GET', self::PREFIX . '/usage/logs', [], $query);
    }

    /**
     * §2.12 POST /internal/v1/subscriptions —— 开通 / 续费（按 v10_host_id 幂等）
     *
     * @param array $payload v10_client_id / v10_host_id / plan_code / period / quota_tokens /
     *                       billing_mode / period_start / period_end / max_keys
     * @return array
     */
    public static function createSubscription(array $payload)
    {
        if (empty($payload['v10_client_id']) || empty($payload['v10_host_id']) || empty($payload['plan_code'])) {
            return self::fail(self::CODE_PARAM_ERROR, 'createSubscription 缺少 v10_client_id / v10_host_id / plan_code', 0);
        }

        return self::request('POST', self::PREFIX . '/subscriptions', $payload);
    }

    /**
     * §2.12 续费（与开通是同一接口；同一 host 再次调用 + 目标周期不同 = 续费）
     *
     * @param array $payload
     * @return array
     */
    public static function renewSubscription(array $payload)
    {
        return self::createSubscription($payload);
    }

    /**
     * §2.13 POST /internal/v1/subscriptions/{id}/suspend
     *
     * @param int    $subscriptionId
     * @param string $reason      overdue|overtraffic|... 或 suspend_reason 文案
     * @param string $suspendType overdue|overtraffic|certification_not_complete|other|downstream
     * @return array
     */
    public static function suspendSubscription($subscriptionId, $reason = 'overdue', $suspendType = 'overdue')
    {
        return self::request(
            'POST',
            self::PREFIX . '/subscriptions/' . (int) $subscriptionId . '/suspend',
            ['reason' => (string) $reason, 'suspend_type' => (string) $suspendType]
        );
    }

    /**
     * §2.14 POST /internal/v1/subscriptions/{id}/resume
     *
     * @param int $subscriptionId
     * @return array
     */
    public static function resumeSubscription($subscriptionId)
    {
        return self::request(
            'POST',
            self::PREFIX . '/subscriptions/' . (int) $subscriptionId . '/resume'
        );
    }

    /**
     * §2.15 DELETE /internal/v1/subscriptions/{id} —— 终止并吊销全部 Key
     *
     * @param int $subscriptionId
     * @return array
     */
    public static function terminateSubscription($subscriptionId)
    {
        return self::request(
            'DELETE',
            self::PREFIX . '/subscriptions/' . (int) $subscriptionId
        );
    }

    /**
     * §2.16 GET /internal/v1/subscriptions/{id}
     *
     * @param int $subscriptionId
     * @return array
     */
    public static function getSubscription($subscriptionId)
    {
        return self::request(
            'GET',
            self::PREFIX . '/subscriptions/' . (int) $subscriptionId
        );
    }

    /**
     * §2.17 GET /internal/v1/billing/host-usage —— 按量计费取数
     *
     * @param int      $v10HostId
     * @param int|null $from
     * @param int|null $to
     * @return array
     */
    public static function hostUsage($v10HostId, $from = null, $to = null)
    {
        $query = ['v10_host_id' => (int) $v10HostId];
        if ($from !== null) {
            $query['from'] = (int) $from;
        }
        if ($to !== null) {
            $query['to'] = (int) $to;
        }

        return self::request('GET', self::PREFIX . '/billing/host-usage', [], $query);
    }

    /**
     * docs/console-api-v1.md §3: POST /internal/v1/console/sso-ticket —— 一次性入驻票据
     *
     * 返回 data = {ticket, expires_in, sso_url}；插件把 sso_url 交给 iframe 壳页面。
     *
     * @param int    $v10ClientId
     * @param string $email
     * @return array
     */
    public static function ssoTicket($v10ClientId, $email)
    {
        return self::request('POST', self::PREFIX . '/console/sso-ticket', [
            'v10_client_id' => (int) $v10ClientId,
            'email'         => (string) $email,
        ]);
    }

    # ==================================================================
    # 三、可靠投递（plan §10.3）
    # ==================================================================

    /**
     * 带落库 + 重试的投递：**先写 _pushlog，再调用网关**
     *
     * 顺序不能反：先落库才保证"网关已加额但本地崩溃"时仍有凭证可回溯；
     * 反过来（先调后写）一旦进程死在两步之间，钱就真的丢了。
     *
     * @param string $path    含 /internal/v1 前缀
     * @param array  $payload 请求体
     * @param string $refType 幂等凭证类型（必填）
     * @param string $refId   幂等凭证 ID（必填）
     * @param string $method  HTTP 方法，默认 POST
     * @return array 网关响应信封；本地拒绝时返回 code=40001
     */
    public static function deliver($path, array $payload, $refType, $refId, $method = 'POST')
    {
        $refType = trim((string) $refType);
        $refId = trim((string) $refId);

        # 绝不发送没有幂等凭证的加额请求（§1.4 40001 是编码错误，本地直接拦）
        if ($refType === '' || $refId === '') {
            return self::fail(self::CODE_MISSING_IDEMPOTENT, '拒绝投递：ref_type / ref_id 为空', 0);
        }

        $pushLogId = PushLogModel::enqueue($path, $payload, $refType, $refId);

        $res = self::request($method, $path, $payload);
        $verdict = self::classify($res);

        if ($verdict === 'ok') {
            PushLogModel::markDone($pushLogId);
        } elseif ($verdict === 'done') {
            # 4xx 且重投无意义：标记完成，但把差异留在 last_error 供人工核对
            PushLogModel::markDone(
                $pushLogId,
                'HTTP ' . ($res['http_status'] ?? 0) . ' code=' . $res['code'] . ' ' . $res['msg']
            );
        } elseif ($verdict === 'stop') {
            PushLogModel::markFailed($pushLogId, '功能开关关闭(50300)：' . $res['msg'], true);
        } else {
            PushLogModel::markFailed($pushLogId, 'HTTP ' . ($res['http_status'] ?? 0) . ' code=' . $res['code'] . ' ' . $res['msg']);
        }

        return $res;
    }

    /**
     * 给账户加/扣额度（可靠投递版）
     *
     * kind 必须落在契约 §2.3 的枚举内：v10_order | redeem_code | admin_adjust | refund
     *
     * @param int    $v10ClientId
     * @param string $kind
     * @param int    $amountMicros
     * @param string $refType
     * @param string $refId
     * @param string $remark
     * @param bool   $allowNegative
     * @return array
     */
    public static function creditAccount(
        $v10ClientId,
        $kind,
        $amountMicros,
        $refType,
        $refId,
        $remark = '',
        $allowNegative = false
    ) {
        $allowedKinds = ['v10_order', 'redeem_code', 'admin_adjust', 'refund'];
        if (!in_array($kind, $allowedKinds, true)) {
            return self::fail(self::CODE_PARAM_ERROR, 'kind 不合法：' . $kind, 0);
        }

        $payload = [
            'v10_client_id'  => (int) $v10ClientId,
            'kind'           => (string) $kind,
            'amount_micros'  => (int) $amountMicros,
            'ref_type'       => (string) $refType,
            'ref_id'         => (string) $refId,
            'allow_negative' => (bool) $allowNegative,
        ];
        if ($remark !== '') {
            $payload['remark'] = (string) $remark;
        }

        return self::deliver(self::PREFIX . '/credit', $payload, $refType, $refId);
    }

    /**
     * 扫描失败队列并重投
     *
     * 由 day_cron / five_minute_cron hook 触发，也可由后台"推送队列"页手动触发。
     *
     * @param int $limit
     * @return array ['scanned'=>int,'done'=>int,'failed'=>int]
     */
    public static function retryFailed($limit = 50)
    {
        $rows = PushLogModel::dueList($limit);
        $stat = ['scanned' => 0, 'done' => 0, 'failed' => 0];

        foreach ($rows as $row) {
            $stat['scanned']++;

            $payload = json_decode((string) $row['payload'], true);
            if (!is_array($payload)) {
                PushLogModel::markFailed((int) $row['id'], 'payload 无法解析，终止重试', true);
                $stat['failed']++;
                continue;
            }

            # 重投同一请求体：ref_type/ref_id 不变，网关侧幂等保证不会重复加额
            $res = self::request('POST', (string) $row['endpoint'], $payload);
            $verdict = self::classify($res);

            if ($verdict === 'ok' || $verdict === 'done') {
                PushLogModel::markDone(
                    (int) $row['id'],
                    $verdict === 'done' ? ('重投命中 ' . $res['code'] . ' ' . $res['msg']) : ''
                );
                $stat['done']++;
            } elseif ($verdict === 'stop') {
                PushLogModel::markFailed((int) $row['id'], '功能开关关闭(50300)：' . $res['msg'], true);
                $stat['failed']++;
            } else {
                PushLogModel::markFailed(
                    (int) $row['id'],
                    'HTTP ' . ($res['http_status'] ?? 0) . ' code=' . $res['code'] . ' ' . $res['msg']
                );
                $stat['failed']++;
            }
        }

        return $stat;
    }

    /**
     * 后台手动重投单条
     *
     * @param int $pushLogId
     * @return array
     */
    public static function retryOne($pushLogId)
    {
        $row = PushLogModel::find((int) $pushLogId);
        if (empty($row)) {
            return self::fail(self::CODE_NOT_FOUND, lang_plugins('llm_gw_pushlog_not_exist'), 0);
        }
        if ((string) $row['status'] === 'done') {
            return self::fail(self::CODE_PARAM_ERROR, lang_plugins('llm_gw_pushlog_already_done'), 0);
        }

        $payload = json_decode((string) $row['payload'], true);
        if (!is_array($payload)) {
            PushLogModel::markFailed((int) $row['id'], 'payload 无法解析，终止重试', true);
            return self::fail(self::CODE_PARAM_ERROR, 'payload 无法解析', 0);
        }

        $res = self::request('POST', (string) $row['endpoint'], $payload);
        $verdict = self::classify($res);

        if ($verdict === 'ok' || $verdict === 'done') {
            PushLogModel::markDone((int) $row['id']);
        } elseif ($verdict === 'stop') {
            PushLogModel::markFailed((int) $row['id'], '功能开关关闭(50300)：' . $res['msg'], true);
        } else {
            PushLogModel::markFailed((int) $row['id'], 'HTTP ' . ($res['http_status'] ?? 0) . ' code=' . $res['code'] . ' ' . $res['msg']);
        }

        return $res;
    }

    # ==================================================================
    # 四、hook 处理器
    # ==================================================================

    /**
     * 支付成功 -> 网关加额度（充值类订单）
     *
     * 流程（plan §10.3）：
     *   1. 读 V10 订单，确认是"充值/余额"类订单（有 host_id 的是产品订单，由模块 createAccount/renew 处理）
     *   2. 写 _txn（本地唯一键 kind+ref_no），已存在则直接跳过 -> 幂等第一道闸门
     *   3. deliver() -> pushlog + 调用 + 重试编排 -> 幂等第二道闸门（网关 (ref_type,ref_id)）
     *
     * // ⚠️ 待实例验证: V10 订单表结构与"充值订单 vs 产品订单"的区分方式未在 idcsmart_ticket 源码中出现。
     * //   当前按 Db::name('order') 读取 id/client_id/amount/host_id，字段名必须在目标实例上核对；
     * //   若实例使用 client_credit 或其它充值入口，需要新增对应 hook（见 README「待验证」）。
     *
     * @param int $orderId
     * @return array
     */
    public static function onOrderPaid($orderId)
    {
        $orderId = (int) $orderId;
        if ($orderId <= 0) {
            return self::fail(self::CODE_PARAM_ERROR, 'onOrderPaid 需要有效 order_id', 0);
        }

        try {
            $order = Db::name('order')->where('id', $orderId)->find();
        } catch (\Throwable $e) {
            return self::fail(self::CODE_INTERNAL, '读取订单失败：' . $e->getMessage(), 0);
        }

        if (empty($order)) {
            return self::fail(self::CODE_NOT_FOUND, '订单不存在：' . $orderId, 0);
        }

        # 产品订单不在这里加余额（套餐额度走 /subscriptions）
        $hostId = (int) ($order['host_id'] ?? 0);
        if ($hostId > 0) {
            return [
                'code'        => self::CODE_OK,
                'msg'         => '产品订单跳过余额加额（由模块开通/续费处理）',
                'data'        => ['skipped' => true],
                'http_status' => 0,
                'raw'         => '',
            ];
        }

        $clientId = (int) ($order['client_id'] ?? 0);
        $amount = isset($order['amount']) ? (float) $order['amount'] : 0.0;
        $amountMicros = LlmGatewayLogic::toMicros($amount);

        if ($clientId <= 0 || $amountMicros === 0) {
            return self::fail(self::CODE_PARAM_ERROR, '订单缺少 client_id 或金额为 0', 0);
        }

        # 幂等第一道闸门：本地单据
        $claim = TxnModel::claim(
            $clientId,
            'v10_order',
            $amountMicros,
            (string) $orderId,
            '订单支付成功'
        );
        if (!$claim['created']) {
            return [
                'code'        => self::CODE_OK,
                'msg'         => '该订单已入账（幂等命中）',
                'data'        => ['applied' => false],
                'http_status' => 0,
                'raw'         => '',
            ];
        }

        $res = self::creditAccount(
            $clientId,
            'v10_order',
            $amountMicros,
            'v10_order',
            (string) $orderId,
            'V10 订单 ' . $orderId . ' 余额充值'
        );

        if ((int) $res['code'] === self::CODE_OK) {
            TxnModel::markStatus($claim['id'], 'done');
        } elseif (in_array(self::classify($res), ['retry'], true)) {
            # 已进重试队列，单据保持 pending，由 retryFailed() 收尾
            TxnModel::markStatus($claim['id'], 'pending');
        } else {
            TxnModel::markStatus($claim['id'], 'failed');
        }

        return $res;
    }

    /**
     * 产品删除 -> 终止订阅并吊销 Key
     *
     * @param mixed $param hook 参数；支持 ['host_id'=>int] 或直接 int
     * @return array
     */
    public static function onHostDeleted($param)
    {
        $hostId = 0;
        if (is_array($param)) {
            $hostId = (int) ($param['host_id'] ?? ($param['id'] ?? 0));
            # V10 可能直接传 HostModel
            if ($hostId === 0 && isset($param['host']) && is_array($param['host'])) {
                $hostId = (int) ($param['host']['id'] ?? 0);
            }
        } elseif (is_numeric($param)) {
            $hostId = (int) $param;
        }

        if ($hostId <= 0) {
            return self::fail(self::CODE_PARAM_ERROR, 'onHostDeleted 未取到 host_id', 0);
        }

        # 本地镜像里拿 client_id（Key 表按 host_id 建了索引）
        $key = KeyModel::where('host_id', $hostId)->find();
        $clientId = empty($key) ? 0 : (int) $key['client_id'];

        $subscriptionId = 0;
        if ($clientId > 0) {
            $subscriptionId = self::findSubscriptionIdByHost($clientId, $hostId);
        }

        if ($subscriptionId > 0) {
            $res = self::terminateSubscription($subscriptionId);
            KeyModel::where('host_id', $hostId)->update([
                'status'      => 'revoked',
                'update_time' => time(),
            ]);

            LlmGatewayLogic::audit('LLM网关：产品#' . $hostId . ' 删除，终止订阅#' . $subscriptionId, $hostId);

            return $res;
        }

        # 拿不到订阅（网关不可达 / 本地无镜像）时至少把本地 Key 标记吊销，避免继续展示可用
        KeyModel::where('host_id', $hostId)->update([
            'status'      => 'revoked',
            'update_time' => time(),
        ]);

        return [
            'code'        => self::CODE_OK,
            'msg'         => '未找到对应订阅（已知 client_id=' . $clientId . '），已本地吊销 Key',
            'data'        => ['host_id' => $hostId, 'subscription_id' => 0],
            'http_status' => 0,
            'raw'         => '',
        ];
    }

    /**
     * 每日任务：失败推送重投 + 用量快照
     *
     * @return array
     */
    public static function dailySync()
    {
        $report = ['retry' => ['scanned' => 0, 'done' => 0, 'failed' => 0], 'usage_keys' => 0];

        # 1) 失败推送重投（最多 200 条/天，避免一次压垮网关）
        $report['retry'] = self::retryFailed(200);

        # 2) 未吊销的 Key 逐把拉 30 天用量，按天 upsert 到本地快照
        $since = time() - 86400 * 30;
        $keys = KeyModel::where('status', '<>', 'revoked')->limit(500)->select()->toArray();

        foreach ($keys as $key) {
            $clientId = (int) $key['client_id'];
            $res = self::usageSummary([
                'v10_client_id' => $clientId,
                'api_key_id'    => (int) $key['key_id'],
                'from'          => $since,
                'to'            => time(),
                'group_by'      => 'day',
            ]);

            if ((int) $res['code'] !== self::CODE_OK || empty($res['data']['buckets'])) {
                continue;
            }

            foreach ($res['data']['buckets'] as $bucket) {
                $day = (string) ($bucket['key'] ?? '');
                if ($day === '') {
                    continue;
                }
                UsageModel::upsertDay(
                    $clientId,
                    (int) $key['host_id'],
                    (int) $key['key_id'],
                    $day,
                    $bucket
                );
                $report['usage_keys']++;
            }
        }

        return $report;
    }

    /**
     * 按 client + host 查网关订阅 ID
     *
     * 契约没有"按 host 查订阅"的接口，只能走 §2.8 quota 的 subscriptions 列表里比对 v10_host_id。
     *
     * @param int $clientId
     * @param int $hostId
     * @return int 0 表示未找到
     */
    public static function findSubscriptionIdByHost($clientId, $hostId)
    {
        $res = self::quota($clientId);
        if ((int) $res['code'] !== self::CODE_OK || empty($res['data']['subscriptions'])) {
            return 0;
        }

        foreach ($res['data']['subscriptions'] as $sub) {
            if ((int) ($sub['v10_host_id'] ?? 0) === (int) $hostId) {
                return (int) ($sub['subscription_id'] ?? 0);
            }
        }

        return 0;
    }
}
