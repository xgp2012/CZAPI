<?php
/**
 * V10 对接层联调脚本（PHP 侧）。
 *
 * 为什么用 PHP 而不只是 Go 单测：签名协议的两端分别是 Go 网关与 PHP 插件，
 * 只有真用 PHP 生成签名去调 Go，才能证明"逐字节一致"这个承诺。Go 单测只能
 * 证明 Go 自己和自己一致。
 *
 * 用法：php scripts/v10-bridge-smoke.php [base_url] [app_id] [app_secret]
 */

declare(strict_types=1);

$baseUrl   = $argv[1] ?? 'http://127.0.0.1:3220';
$appId     = $argv[2] ?? 'v10test';
$appSecret = $argv[3] ?? 'test-secret-0123456789abcdef';

// ------------------------------------------------------------------ 签名
// 与 v10/idcsmart_llm_gateway/model/LlmApi.php::sign 完全一致的算法。

function cz_canonical_query(array $query): string
{
    if ($query === []) {
        return '';
    }
    ksort($query, SORT_STRING);
    $parts = [];
    foreach ($query as $key => $value) {
        $parts[] = rawurlencode((string) $key) . '=' . rawurlencode((string) $value);
    }
    return implode('&', $parts);
}

function cz_sign(
    string $secret,
    string $method,
    string $path,
    string $sortedQuery,
    string $body,
    int $timestamp,
    string $nonce
): string {
    $raw = implode("\n", [
        strtoupper($method),
        $path,
        $sortedQuery,
        hash('sha256', $body),
        (string) $timestamp,
        $nonce,
    ]);
    return hash_hmac('sha256', $raw, $secret);
}

function cz_nonce(): string
{
    return bin2hex(random_bytes(16));
}

/** @return array{status:int, body:string, json:?array} */
function cz_call(
    string $baseUrl,
    string $appId,
    string $appSecret,
    string $method,
    string $path,
    array $query = [],
    ?array $payload = null
): array {
    $body = $payload === null ? '' : json_encode($payload, JSON_UNESCAPED_UNICODE);
    if ($body === false) {
        throw new RuntimeException('failed to encode payload');
    }
    $sortedQuery = cz_canonical_query($query);
    $timestamp   = time();
    $nonce       = cz_nonce();
    $signature   = cz_sign($appSecret, $method, $path, $sortedQuery, $body, $timestamp, $nonce);

    $url = rtrim($baseUrl, '/') . $path;
    if ($sortedQuery !== '') {
        $url .= '?' . $sortedQuery;
    }

    $ch = curl_init($url);
    $headers = [
        'Content-Type: application/json; charset=utf-8',
        'X-V10-App-Id: ' . $appId,
        'X-V10-Timestamp: ' . $timestamp,
        'X-V10-Nonce: ' . $nonce,
        'X-V10-Signature: ' . $signature,
    ];
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST  => strtoupper($method),
        CURLOPT_HTTPHEADER     => $headers,
        CURLOPT_TIMEOUT        => 20,
    ]);
    if ($body !== '') {
        curl_setopt($ch, CURLOPT_POSTFIELDS, $body);
    }
    $responseBody = (string) curl_exec($ch);
    $status       = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $curlError    = curl_error($ch);
    curl_close($ch);

    if ($curlError !== '') {
        throw new RuntimeException('curl error: ' . $curlError);
    }

    return [
        'status' => $status,
        'body'   => $responseBody,
        'json'   => json_decode($responseBody, true) ?: null,
    ];
}

// ------------------------------------------------------------------ 断言

$passed = 0;
$failed = 0;

function check(string $name, bool $ok, string $detail = ''): void
{
    global $passed, $failed;
    if ($ok) {
        $passed++;
        echo "  [PASS] {$name}\n";
        return;
    }
    $failed++;
    echo "  [FAIL] {$name}" . ($detail !== '' ? "  -> {$detail}" : '') . "\n";
}

function show(string $label, array $result): void
{
    echo "  {$label}: HTTP {$result['status']}  " . substr($result['body'], 0, 260) . "\n";
}

// ------------------------------------------------------------------ 用例

echo "== V10 bridge smoke test ==\n";
echo "   base: {$baseUrl}\n\n";

echo "-- 1. 健康探测（免签名） --\n";
$health = cz_call($baseUrl, $appId, $appSecret, 'GET', '/internal/v10/health');
show('health', $health);
check('health returns code 0', ($health['json']['code'] ?? -1) === 0);

echo "\n-- 2. 账户同步 --\n";
$sync = cz_call($baseUrl, $appId, $appSecret, 'POST', '/internal/v10/accounts/sync', [], [
    'client_id'    => 1001,
    'display_name' => 'test-client',
    'email'        => 'test@example.com',
]);
show('sync', $sync);
check('sync ok', ($sync['json']['code'] ?? -1) === 0);
$userId = $sync['json']['data']['user_id'] ?? 0;
check('sync returns user_id', $userId > 0, "user_id={$userId}");

echo "\n-- 3. 幂等加额：100 元 --\n";
$orderRef = 'ORD-20261105-' . substr((string) time(), -6);
$creditPayload = [
    'client_id'     => 1001,
    'kind'          => 'v10_order',
    'amount_micros' => 100000000,
    'ref_type'      => 'v10_order',
    'ref_id'        => $orderRef,
    'remark'        => 'balance topup 100 CNY',
];
$credit1 = cz_call($baseUrl, $appId, $appSecret, 'POST', '/internal/v10/credit', [], $creditPayload);
show('credit#1', $credit1);
check('first credit applied', ($credit1['json']['data']['applied'] ?? false) === true);

echo "\n-- 4. 同一凭证重放（幂等） --\n";
$credit2 = cz_call($baseUrl, $appId, $appSecret, 'POST', '/internal/v10/credit', [], $creditPayload);
show('credit#2', $credit2);
check('replay is idempotent (applied=false)', ($credit2['json']['data']['applied'] ?? true) === false);
check(
    'replay keeps balance unchanged',
    ($credit1['json']['data']['balance_after'] ?? 0) === ($credit2['json']['data']['balance_after'] ?? -1),
    'first=' . ($credit1['json']['data']['balance_after'] ?? 'null') . ' second=' . ($credit2['json']['data']['balance_after'] ?? 'null')
);

echo "\n-- 5. 查询额度 --\n";
$quota = cz_call($baseUrl, $appId, $appSecret, 'GET', '/internal/v10/accounts/quota', ['client_id' => 1001]);
show('quota', $quota);
check('quota ok', ($quota['json']['code'] ?? -1) === 0);
check('balance is positive', ($quota['json']['data']['balance_quota'] ?? 0) > 0);

echo "\n-- 6. 创建 Key（明文只此一次） --\n";
$keyCreate = cz_call($baseUrl, $appId, $appSecret, 'POST', '/internal/v10/keys', [], [
    'client_id' => 1001,
    'name'      => 'production',
    'models'    => ['deepseek-chat'],
    'max_keys'  => 5,
]);
show('key.create', $keyCreate);
$plainKey = $keyCreate['json']['data']['key'] ?? '';
check('key create ok', ($keyCreate['json']['code'] ?? -1) === 0);
check('key is plaintext sk-...', str_starts_with((string) $plainKey, 'sk-'), 'got=' . substr((string) $plainKey, 0, 8));
check('key prefix returned', ($keyCreate['json']['data']['key_prefix'] ?? '') !== '');

echo "\n-- 7. Key 列表（不得含明文） --\n";
$keyList = cz_call($baseUrl, $appId, $appSecret, 'GET', '/internal/v10/keys', ['client_id' => 1001]);
show('key.list', $keyList);
check('key list ok', ($keyList['json']['code'] ?? -1) === 0);
$listJson = json_encode($keyList['json'], JSON_UNESCAPED_UNICODE);
check('key list does not leak plaintext', $plainKey === '' || !str_contains($listJson, $plainKey));

echo "\n-- 8. 错误签名必须被拒 --\n";
$badSig = cz_call($baseUrl, $appId, 'definitely-the-wrong-secret', 'GET', '/internal/v10/keys', ['client_id' => 1001]);
show('bad signature', $badSig);
check('bad signature -> 401', $badSig['status'] === 401, 'status=' . $badSig['status']);
check('bad signature -> code 40100', ($badSig['json']['code'] ?? 0) === 40100);

echo "\n-- 9. 缺少幂等凭证必须被拒 --\n";
$missingRef = cz_call($baseUrl, $appId, $appSecret, 'POST', '/internal/v10/credit', [], [
    'client_id'     => 1001,
    'amount_micros' => 1000000,
]);
show('missing ref', $missingRef);
check('missing ref -> 40001', ($missingRef['json']['code'] ?? 0) === 40001);

echo "\n-- 10. 未知 app_id 必须被拒 --\n";
$unknown = cz_call($baseUrl, 'no-such-app', $appSecret, 'GET', '/internal/v10/keys', ['client_id' => 1001]);
show('unknown app_id', $unknown);
check('unknown app_id -> 401', $unknown['status'] === 401, 'status=' . $unknown['status']);

echo "\n-- 11. 超额创建 Key 触发上限 --\n";
$limitHit = false;
for ($i = 0; $i < 8; $i++) {
    $extra = cz_call($baseUrl, $appId, $appSecret, 'POST', '/internal/v10/keys', [], [
        'client_id' => 1001,
        'name'      => 'bulk-' . $i,
        'max_keys'  => 3,
    ]);
    if (($extra['json']['code'] ?? 0) !== 0) {
        $limitHit = true;
        show('limit reached at iteration ' . $i, $extra);
        break;
    }
}
check('key limit is enforced', $limitHit);

echo "\n==================================\n";
echo "  passed: {$passed}   failed: {$failed}\n";
exit($failed === 0 ? 0 : 1);
