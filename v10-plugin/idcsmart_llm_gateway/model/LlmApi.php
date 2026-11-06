<?php
namespace addon\idcsmart_llm_gateway\model;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use think\facade\Db;

/**
 * 网关内部接口客户端（V10 插件 -> 网关，服务端到服务端）
 *
 * ============================================================================
 * 契约来源：网关仓库 internalv10 包（/internal/v10）。
 *   - 统一前缀 /internal/v10
 *   - 响应信封 {"code":0,"msg":"ok","data":{...}}
 *   - 请求头 X-V10-App-Id / X-V10-Timestamp / X-V10-Nonce / X-V10-Signature
 *   - 待签串 = METHOD\nPATH\nSORTED_QUERY\nSHA256_HEX(BODY)\nTIMESTAMP\nNONCE
 *   - 签名   = hex(HMAC_SHA256(app_secret, 待签串))
 *
 * 与旧版 /internal/v1 的差异（本次二开的关键变化）：
 *   1. 前缀 /internal/v1 -> /internal/v10
 *   2. 请求头 X-CZ-* -> X-V10-*
 *   3. 账户标识统一为 client_id（旧版叫 v10_client_id）
 *   4. 金额统一用整数微元 amount_micros（1 元 = 1_000_000）
 *   5. 订阅语义改为 host：POST /hosts 绑定、/hosts/:id/status 改状态
 *   6. 新增单点登录 POST /sso/ticket
 * ============================================================================
 *
 * @author  CZAPI
 * @version 2.0.0
 */
class LlmApi
{
    # ------------------------------------------------------------------
    # 业务码，与网关 internalv10/middleware.go 的 Code* 常量一一对应
    # ------------------------------------------------------------------
    const CODE_OK              = 0;
    const CODE_PARAM_ERROR     = 40000;
    const CODE_MISSING_REF     = 40001;
    const CODE_SIGN_INVALID    = 40100;
    const CODE_TIMESTAMP_WRONG = 40101;
    const CODE_NONCE_REPLAY    = 40102;
    const CODE_SITE_DISABLED   = 40300;
    const CODE_NOT_FOUND       = 40400;
    const CODE_REF_CONFLICT    = 40900;
    const CODE_INSUFFICIENT    = 40200;
    const CODE_RATE_LIMITED    = 42900;
    const CODE_INTERNAL        = 50000;
    const CODE_FEATURE_OFF     = 50300;

    # 传输层失败（不是网关返回的业务码）
    const CODE_NETWORK      = -1;
    const CODE_BAD_RESPONSE = -2;

    const PREFIX = '/internal/v10';

    # ==================================================================
    # 一、签名与请求
    # ==================================================================

    /**
     * 计算签名（必须与 Go 侧逐字节一致）
     *
     *   $raw = implode("\n", [strtoupper($method), $path, $sortedQuery,
     *                         hash('sha256', $body), (string)$ts, $nonce]);
     *   return hash_hmac('sha256', $raw, $secret);
     *
     * @param string       $method
     * @param string       $path
     * @param array|string $query  数组会先 canonicalQuery；已排序字符串原样使用
     * @param string       $body
     * @param int          $ts
     * @param string       $nonce
     * @param string|null  $secret 留空取配置 app_secret
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
     * 查询串规范化：key 按 ASCII 升序，key 与 value 都用 rawurlencode
     *
     * 网关侧刻意用 %20 而不是 '+' 来编码空格，正好与 PHP 的 rawurlencode 一致，
     * 所以含空格的参数（例如 Key 名称）两端签名相同。
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
     * @param string $path    含 /internal/v10 前缀的路径
     * @param array  $payload 请求体（空数组 = 无请求体）
     * @param array  $query   查询参数
     * @return array [
     *   'code'        => int,
     *   'msg'         => string,
     *   'data'        => mixed,
     *   'http_status' => int,
     *   'raw'         => string,
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

        # 空数组 = 空体（此时签名里是 sha256("")）
        $body = empty($payload)
            ? ''
            : json_encode($payload, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);

        $sortedQuery = self::canonicalQuery($query);
        $url = $baseUrl . $path . ($sortedQuery !== '' ? '?' . $sortedQuery : '');

        $ts = time();
        $nonce = bin2hex(random_bytes(16));
        $signature = self::sign($method, $path, $sortedQuery, $body, $ts, $nonce, $secret);

        $headers = [
            'X-V10-App-Id: ' . $appId,
            'X-V10-Timestamp: ' . $ts,
            'X-V10-Nonce: ' . $nonce,
            'X-V10-Signature: ' . $signature,
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

        # 开了 CURLOPT_HEADER，按头长度切掉响应头
        $responseBody = substr($raw, $headerSize);

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
     * 按业务码对一次请求结果分类（可靠投递用）
     *
     *   'ok'    -> 成功
     *   'done'  -> 4xx 且重投无意义，标记完成并记录差异
     *   'retry' -> 429 / 5xx / 网络错误 / 校时失败，进指数退避
     *   'stop'  -> 503 功能关闭，停止重试并告警
     *
     * @param array $res
     * @return string
     */
    public static function classify(array $res)
    {
        $code = (int) ($res['code'] ?? self::CODE_INTERNAL);
        $http = (int) ($res['http_status'] ?? 0);

        if ($code === self::CODE_NETWORK) {
            return 'retry';
        }
        if ($code === self::CODE_OK && ($http === 0 || $http < 400)) {
            return 'ok';
        }
        # nonce 重放说明网关已处理过同一请求，按完成处理。
        if ($code === self::CODE_NONCE_REPLAY) {
            return 'done';
        }
        if ($code === self::CODE_FEATURE_OFF) {
            return 'stop';
        }
        # 时间戳超窗通常是对端时钟漂移，校时后重试一次可能成功。
        if ($code === self::CODE_TIMESTAMP_WRONG) {
            return 'retry';
        }
        if ($code === self::CODE_RATE_LIMITED) {
            return 'retry';
        }
        if ($http >= 500 || $code === self::CODE_INTERNAL) {
            return 'retry';
        }

        return 'done';
    }

    # ==================================================================
    # 二、类型化封装
    # ==================================================================

    /**
     * GET /internal/v10/health —— 健康探测（配置页"测试连接"用，免签名）
     *
     * @return array
     */
    public static function health()
    {
        return self::request('GET', self::PREFIX . '/health');
    }

    /**
     * POST /internal/v10/accounts/sync —— 创建或确保账户存在
     *
     * @param int    $clientId
     * @param string $displayName
     * @param string $email
     * @return array
     */
    public static function syncAccount($clientId, $displayName = '', $email = '')
    {
        return self::request('POST', self::PREFIX . '/accounts/sync', [
            'client_id'    => (int) $clientId,
            'display_name' => (string) $displayName,
            'email'        => (string) $email,
        ]);
    }

    /**
     * GET /internal/v10/accounts/quota —— 账户余额与订阅明细
     *
     * @param int $clientId
     * @return array
     */
    public static function quota($clientId)
    {
        return self::request('GET', self::PREFIX . '/accounts/quota', [], [
            'client_id' => (int) $clientId,
        ]);
    }

    /**
     * POST /internal/v10/credit —— 加/扣额度（原样调用，不做重试编排）
     *
     * 需要"不丢 + 幂等 + 重试"时用 creditAccount()。
     *
     * @param array $payload client_id / amount_micros / ref_type / ref_id / kind / remark
     * @return array
     */
    public static function credit(array $payload)
    {
        # 缺幂等凭证是编码错误，本地先拦下，不浪费一次网络往返。
        if (empty($payload['ref_type']) || empty($payload['ref_id'])) {
            return self::fail(self::CODE_MISSING_REF, '缺少幂等凭证 ref_type / ref_id，拒绝发送', 0);
        }

        return self::request('POST', self::PREFIX . '/credit', $payload);
    }

    /**
     * POST /internal/v10/keys —— 创建 Key（明文只返回这一次）
     *
     * @param array $payload client_id / host_id / name / models / ip_whitelist / expires_at / max_keys
     * @return array
     */
    public static function createKey(array $payload)
    {
        return self::request('POST', self::PREFIX . '/keys', $payload);
    }

    /**
     * GET /internal/v10/keys —— Key 列表（永不含明文）
     *
     * @param int      $clientId
     * @param int|null $hostId
     * @return array
     */
    public static function listKeys($clientId, $hostId = null)
    {
        $query = ['client_id' => (int) $clientId];
        if ($hostId !== null && (int) $hostId > 0) {
            $query['host_id'] = (int) $hostId;
        }

        return self::request('GET', self::PREFIX . '/keys', [], $query);
    }

    /**
     * PATCH /internal/v10/keys/{id} —— 部分更新
     *
     * 只允许 name / status / models / ip_whitelist / expires_at，其余键本地丢弃。
     * client_id 必填：网关用它校验这把 Key 确实属于该客户，防止越权改成别人的 Key。
     *
     * @param int   $clientId
     * @param int   $keyId
     * @param array $fields
     * @return array
     */
    public static function updateKey($clientId, $keyId, array $fields)
    {
        $allowed = ['name', 'status', 'models', 'ip_whitelist', 'expires_at'];
        $payload = ['client_id' => (int) $clientId];
        foreach ($allowed as $key) {
            if (array_key_exists($key, $fields)) {
                $payload[$key] = $fields[$key];
            }
        }

        if (count($payload) === 1) {
            return self::fail(self::CODE_PARAM_ERROR, '没有可更新的字段', 0);
        }

        return self::request('PATCH', self::PREFIX . '/keys/' . (int) $keyId, $payload);
    }

    /**
     * DELETE /internal/v10/keys/{id} —— 吊销（幂等）
     *
     * client_id 必填，理由同上：网关要校验 Key 归属。
     *
     * @param int $clientId
     * @param int $keyId
     * @return array
     */
    public static function deleteKey($clientId, $keyId)
    {
        return self::request('DELETE', self::PREFIX . '/keys/' . (int) $keyId, [], [
            'client_id' => (int) $clientId,
        ]);
    }

    /**
     * GET /internal/v10/models —— 可售模型目录
     *
     * @return array
     */
    public static function models()
    {
        return self::request('GET', self::PREFIX . '/models');
    }

    /**
     * GET /internal/v10/usage/summary —— 用量合计 + 分组明细
     *
     * 分组在网关侧算好后返回，插件不再自行聚合，避免两边口径不一致。
     * 刻意不返回请求正文：账单页不需要，正文属于客户隐私。
     *
     * @param array $params client_id(必填) / from / to / group_by(day|model|token)
     * @return array
     */
    public static function usageSummary(array $params)
    {
        if (empty($params['client_id'])) {
            return self::fail(self::CODE_PARAM_ERROR, 'usageSummary 缺少 client_id', 0);
        }

        $query = [];
        foreach (['client_id', 'from', 'to', 'group_by'] as $key) {
            if (isset($params[$key]) && $params[$key] !== '' && $params[$key] !== null) {
                $query[$key] = $params[$key];
            }
        }

        return self::request('GET', self::PREFIX . '/usage/summary', [], $query);
    }

    /**
     * GET /internal/v10/usage/logs —— 明细分页（不含请求正文）
     *
     * @param array $params client_id(必填) / from / to / page / page_size / model
     * @return array
     */
    public static function usageLogs(array $params)
    {
        if (empty($params['client_id'])) {
            return self::fail(self::CODE_PARAM_ERROR, 'usageLogs 缺少 client_id', 0);
        }

        $query = [];
        foreach (['client_id', 'from', 'to', 'page', 'page_size', 'model'] as $key) {
            if (isset($params[$key]) && $params[$key] !== '' && $params[$key] !== null) {
                $query[$key] = $params[$key];
            }
        }
        # 网关侧上限 200，这里先夹住，避免一次拉回整年日志。
        if (isset($query['page_size'])) {
            $pageSize = (int) $query['page_size'];
            $query['page_size'] = $pageSize > 200 ? 200 : max(1, $pageSize);
        }

        return self::request('GET', self::PREFIX . '/usage/logs', [], $query);
    }

    /**
     * POST /internal/v10/hosts —— 商品实例绑定/续期
     *
     * 已绑定的 host 会原地续期，不会重复开订阅。
     *
     * @param array $payload client_id / host_id / plan_id / duration_days / ref_type / ref_id
     * @return array
     */
    public static function bindHost(array $payload)
    {
        return self::request('POST', self::PREFIX . '/hosts', $payload);
    }

    /**
     * POST /internal/v10/hosts/{id}/status —— 暂停/恢复/终止
     *
     * 暂停与终止会同时禁用该 host 下所有 Key，避免"欠费还能用"。
     *
     * @param int    $hostId
     * @param string $status active|suspended|terminated
     * @param string $reason
     * @return array
     */
    public static function setHostStatus($hostId, $status, $reason = '')
    {
        return self::request('POST', self::PREFIX . '/hosts/' . (int) $hostId . '/status', [
            'status' => (string) $status,
            'reason' => (string) $reason,
        ]);
    }

    /**
     * GET /internal/v10/hosts/{id}/status
     *
     * @param int $hostId
     * @return array
     */
    public static function hostStatus($hostId)
    {
        return self::request('GET', self::PREFIX . '/hosts/' . (int) $hostId . '/status');
    }

    /**
     * POST /internal/v10/sso/ticket —— 申请一次性控制台登录票据
     *
     * 返回 data.ticket 与 data.login_url。login_url 直接把浏览器指过去即可，
     * 票据放在 fragment 里，不会进网关访问日志。
     *
     * @param int $clientId
     * @return array
     */
    public static function ssoTicket($clientId)
    {
        return self::request('POST', self::PREFIX . '/sso/ticket', [
            'client_id' => (int) $clientId,
        ]);
    }

    # ==================================================================
    # 三、可靠投递
    # ==================================================================

    /**
     * 带落库的投递：先写 pushlog(pending) -> 立即发一次 -> 按结果分类
     *
     * 顺序不能颠倒。先落库再请求，进程在请求途中被杀也能在下次重投时补上；
     * 先请求后落库则会丢单。
     *
     * @param string $path    含前缀的路径，如 /internal/v10/credit
     * @param array  $payload
     * @param string $refType
     * @param string $refId
     * @param string $method
     * @return array
     */
    public static function deliver($path, array $payload, $refType, $refId, $method = 'POST')
    {
        $refType = trim((string) $refType);
        $refId = trim((string) $refId);

        # 没有幂等凭证的加额请求绝不发送：网关会返回 40001，但更该在本地拦住。
        if ($refType === '' || $refId === '') {
            return self::fail(self::CODE_MISSING_REF, '拒绝投递：ref_type / ref_id 为空', 0);
        }

        $pushLogId = PushLogModel::enqueue($path, $payload, $refType, $refId, $method);

        $res = self::request($method, $path, $payload);
        $verdict = self::classify($res);

        if ($verdict === 'ok') {
            PushLogModel::markDone($pushLogId);
        } elseif ($verdict === 'done') {
            # 4xx 且重投无意义：标记完成，但把差异留在 last_error 供人工核对。
            PushLogModel::markDone(
                $pushLogId,
                'HTTP ' . ($res['http_status'] ?? 0) . ' code=' . $res['code'] . ' ' . $res['msg']
            );
        } elseif ($verdict === 'stop') {
            PushLogModel::markFailed($pushLogId, '功能开关关闭(50300)：' . $res['msg'], true);
        } else {
            PushLogModel::markFailed(
                $pushLogId,
                'HTTP ' . ($res['http_status'] ?? 0) . ' code=' . $res['code'] . ' ' . $res['msg']
            );
        }

        return $res;
    }

    /**
     * 给账户加/扣额度（可靠投递版）
     *
     * kind 必须落在网关接受的枚举内：v10_order | redeem_code | admin_adjust | refund
     *
     * @param int    $clientId
     * @param string $kind
     * @param int    $amountMicros
     * @param string $refType
     * @param string $refId
     * @param string $remark
     * @param bool   $allowNegative
     * @return array
     */
    public static function creditAccount(
        $clientId,
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
            'client_id'      => (int) $clientId,
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
     * 由定时任务 hook 触发，也可由后台"推送队列"页手动触发。
     * 重投的请求体原样不动，ref_type/ref_id 不变，网关侧幂等索引保证不会重复加额。
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

            $res = self::request(self::methodOf($row), (string) $row['endpoint'], $payload);
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
     * 取出待重投记录原本用的 HTTP 方法
     *
     * 早期重投一律写死 POST，于是任何非 POST 的投递（例如未来加了 GET 的查询型
     * 补偿）一旦失败就永远重投不成功。老数据没有 method 列值时回落到 POST，
     * 保持与历史行为一致。
     *
     * @param array $row
     * @return string
     */
    private static function methodOf(array $row)
    {
        $method = strtoupper(trim((string) ($row['method'] ?? '')));

        return $method !== '' ? $method : 'POST';
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
            return self::fail(self::CODE_NOT_FOUND, '推送记录不存在', 0);
        }
        if ((string) $row['status'] === 'done') {
            return self::fail(self::CODE_PARAM_ERROR, '该记录已投递成功，无需重投', 0);
        }

        $payload = json_decode((string) $row['payload'], true);
        if (!is_array($payload)) {
            PushLogModel::markFailed((int) $row['id'], 'payload 无法解析，终止重试', true);
            return self::fail(self::CODE_PARAM_ERROR, 'payload 无法解析', 0);
        }

        $res = self::request(self::methodOf($row), (string) $row['endpoint'], $payload);
        $verdict = self::classify($res);

        if ($verdict === 'ok' || $verdict === 'done') {
            PushLogModel::markDone((int) $row['id']);
        } elseif ($verdict === 'stop') {
            PushLogModel::markFailed((int) $row['id'], '功能开关关闭(50300)：' . $res['msg'], true);
        } else {
            PushLogModel::markFailed(
                (int) $row['id'],
                'HTTP ' . ($res['http_status'] ?? 0) . ' code=' . $res['code'] . ' ' . $res['msg']
            );
        }

        return $res;
    }

    # ==================================================================
    # 四、V10 钩子处理
    # ==================================================================

    /**
     * 支付成功 -> 网关加余额
     *
     * 只处理"充值/调账"类订单；带 host_id 的产品订单由模块的 createAccount / renew 处理，
     * 这里主动跳过，避免套餐额度与余额被加两次。
     *
     * 双重幂等：
     *   1. TxnModel::claim —— 本地唯一键 (kind, ref_no)，同一订单只登记一次
     *   2. 网关 v10_refs 的 (site_id, ref_type, ref_id) 唯一索引
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

        # 产品订单不在这里加余额：套餐额度走 /hosts。
        if ((int) ($order['host_id'] ?? 0) > 0) {
            return [
                'code'        => self::CODE_OK,
                'msg'         => '产品订单跳过余额加额（由模块开通/续费处理）',
                'data'        => ['skipped' => true],
                'http_status' => 0,
                'raw'         => '',
            ];
        }

        $clientId = (int) ($order['client_id'] ?? 0);
        $amountMicros = LlmGatewayLogic::toMicros($order['amount'] ?? 0);

        if ($clientId <= 0 || $amountMicros === 0) {
            return self::fail(self::CODE_PARAM_ERROR, '订单缺少 client_id 或金额为 0', 0);
        }

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
        } elseif (self::classify($res) === 'retry') {
            # 已进重试队列，单据保持 pending，由 retryFailed() 收尾。
            TxnModel::markStatus($claim['id'], 'pending');
        } else {
            TxnModel::markStatus($claim['id'], 'failed');
        }

        return $res;
    }

    /**
     * 产品删除 -> 终止 host 并吊销 Key
     *
     * 与模块的 terminateAccount() 是两条路径：模块走正常删除流程，本方法是最兜底的一道，
     * 防止后台强制删除、批量清理时漏吊销。两者都幂等。
     *
     * @param mixed $param
     * @return array
     */
    public static function onHostDeleted($param)
    {
        $hostId = 0;
        if (is_array($param)) {
            $hostId = (int) ($param['host_id'] ?? ($param['id'] ?? 0));
            if ($hostId === 0 && isset($param['host']) && is_array($param['host'])) {
                $hostId = (int) ($param['host']['id'] ?? 0);
            }
        } elseif (is_object($param)) {
            $hostId = (int) ($param->id ?? ($param->host_id ?? 0));
        } elseif (is_numeric($param)) {
            $hostId = (int) $param;
        }

        if ($hostId <= 0) {
            return self::fail(self::CODE_PARAM_ERROR, 'onHostDeleted 未取到 host_id', 0);
        }

        $res = self::setHostStatus($hostId, 'terminated', 'V10 产品删除');

        # 网关不可达或 host 从未绑定，则至少把本地镜像标成吊销，避免继续展示"可用"。
        if ((int) $res['code'] !== self::CODE_OK) {
            KeyModel::where('host_id', $hostId)->update([
                'status'      => 'revoked',
                'update_time' => time(),
            ]);
        }

        LlmGatewayLogic::audit('LLM网关：产品#' . $hostId . ' 删除，已终止并吊销 Key', $hostId);

        return $res;
    }

    /**
     * 定时任务：失败投递重投 + 用量快照
     *
     * 用量快照让网关不可达时前台仍能显示"近 30 天趋势"，
     * 属于只读降级数据，不参与计费。
     *
     * @return array
     */
    public static function dailySync()
    {
        $report = ['retry' => ['scanned' => 0, 'done' => 0, 'failed' => 0], 'usage_keys' => 0];

        $report['retry'] = self::retryFailed(200);

        $since = time() - 86400 * 30;
        $now = time();

        /*
         * 只取"有未吊销 Key 的客户"去重后的 client_id。
         *
         * 早期写法是 KeyModel::limit(500)->select() 再按 client 去重：
         * 客户一大（500 把 Key 以上）就永远轮不到后面的客户，快照长期缺失；
         * 而这里真正需要的是**客户列表**，逐把 Key 只需要 client_id 一列。
         * 改为直接取 distinct client_id 并分页，避免被 Key 数量绑架。
         */
        $clientIds = [];
        $page = 1;
        $pageSize = 500;
        while (true) {
            $rows = KeyModel::where('status', '<>', 'revoked')
                ->where('client_id', '>', 0)
                ->distinct(true)
                ->field('client_id')
                ->order('client_id', 'asc')
                ->page($page, $pageSize)
                ->select()
                ->toArray();

            if (empty($rows)) {
                break;
            }
            foreach ($rows as $row) {
                $clientIds[(int) $row['client_id']] = true;
            }
            if (count($rows) < $pageSize) {
                break;
            }
            $page++;
            // 硬上限：避免异常数据让定时任务跑不完，3000 个客户足够当前规模。
            if ($page > 6) {
                break;
            }
        }

        foreach (array_keys($clientIds) as $clientId) {
            if ($clientId <= 0) {
                continue;
            }

            $res = self::usageSummary([
                'client_id' => $clientId,
                'from'      => $since,
                'to'        => $now,
                'group_by'  => 'day',
            ]);
            if ((int) $res['code'] !== self::CODE_OK) {
                continue;
            }

            foreach (($res['data']['buckets'] ?? []) as $bucket) {
                $day = (string) ($bucket['key'] ?? '');
                if ($day === '') {
                    continue;
                }
                # host_id/key_id 传 0 表示"客户维度汇总"，与 usageSummary 的口径一致。
                UsageModel::upsertDay($clientId, 0, 0, $day, $bucket);
                $report['usage_keys']++;
            }
        }

        return $report;
    }
}
