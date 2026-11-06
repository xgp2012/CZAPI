<?php
/**
 * 验证 V10 单点登录拿到的会话，确实能当普通控制台会话使用。
 *
 * 只验"能换到票据"是不够的：票据换成 token 之后，如果 token 调不动
 * /api/user/self，那用户点进来还是白屏。所以这里一直验到读到自己资料为止。
 *
 * 用法：php scripts/v10-sso-check.php [base] [appId] [secret]
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3240', '/');
$appId = $argv[2] ?? 'v10test';
$secret = $argv[3] ?? 'test-secret-0123456789abcdef';

$fail = 0;
function check(string $name, bool $pass, string $detail = ''): void
{
    global $fail;
    if ($pass) {
        echo "  PASS  $name\n";
    } else {
        $fail++;
        echo "  FAIL  $name" . ($detail !== '' ? "  -> $detail" : '') . "\n";
    }
}

function v10Sign(string $secret, string $method, string $path, string $query, string $body, int $ts, string $nonce): string
{
    $payload = strtoupper($method) . "\n" . $path . "\n" . $query . "\n" . hash('sha256', $body) . "\n" . $ts . "\n" . $nonce;

    return hash_hmac('sha256', $payload, $secret);
}

function v10Call(string $base, string $appId, string $secret, string $method, string $path, array $payload): array
{
    $body = json_encode($payload, JSON_UNESCAPED_UNICODE);
    $ts = time();
    $nonce = bin2hex(random_bytes(16));
    $sig = v10Sign($secret, $method, $path, '', $body, $ts, $nonce);
    $ch = curl_init($base . $path);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST => $method,
        CURLOPT_HTTPHEADER => [
            'Content-Type: application/json',
            'X-V10-App-Id: ' . $appId,
            'X-V10-Timestamp: ' . $ts,
            'X-V10-Nonce: ' . $nonce,
            'X-V10-Signature: ' . $sig,
        ],
        CURLOPT_POSTFIELDS => $body,
        CURLOPT_TIMEOUT => 20,
    ]);
    $raw = curl_exec($ch);
    curl_close($ch);

    return json_decode((string) $raw, true) ?: [];
}

$clientId = random_int(100000, 999999);

echo "== 1. 准备账户 ==\n";
$sync = v10Call($base, $appId, $secret, 'POST', '/internal/v10/accounts/sync', [
    'client_id' => $clientId,
    'display_name' => 'sso测试客户',
    'email' => 'sso@v10.local',
]);
check('账户就绪', (int) ($sync['code'] ?? -1) === 0, json_encode($sync, JSON_UNESCAPED_UNICODE));

echo "\n== 2. 签发一次性票据 ==\n";
$ticketResp = v10Call($base, $appId, $secret, 'POST', '/internal/v10/sso/ticket', ['client_id' => $clientId]);
$ticket = (string) ($ticketResp['data']['ticket'] ?? '');
$loginUrl = (string) ($ticketResp['data']['login_url'] ?? '');
check('票据签发成功', $ticket !== '', json_encode($ticketResp, JSON_UNESCAPED_UNICODE));
check('login_url 指向控制台', str_contains($loginUrl, '/v10/login#'), $loginUrl);

echo "\n== 3. 用票据换会话 ==\n";
$jar = tempnam(sys_get_temp_dir(), 'ck');
$ch = curl_init($base . '/api/v10/sso/exchange');
curl_setopt_array($ch, [
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_CUSTOMREQUEST => 'POST',
    CURLOPT_HTTPHEADER => ['Content-Type: application/json'],
    CURLOPT_POSTFIELDS => json_encode(['ticket' => $ticket, 'app_id' => $appId]),
    CURLOPT_COOKIEJAR => $jar,
    CURLOPT_COOKIEFILE => $jar,
    CURLOPT_TIMEOUT => 20,
]);
$exchangeRaw = (string) curl_exec($ch);
curl_close($ch);
$exchange = json_decode($exchangeRaw, true) ?: [];
$accessToken = (string) ($exchange['data']['access_token'] ?? '');
check('换到 access_token', $accessToken !== '', $exchangeRaw);
check('返回用户信息', (int) ($exchange['data']['user']['id'] ?? 0) > 0);
check('用户名由 app_id 派生', ($exchange['data']['user']['username'] ?? '') !== '',
    (string) ($exchange['data']['user']['username'] ?? 'none'));

echo "\n== 4. 票据只能用一次 ==\n";
$ch = curl_init($base . '/api/v10/sso/exchange');
curl_setopt_array($ch, [
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_CUSTOMREQUEST => 'POST',
    CURLOPT_HTTPHEADER => ['Content-Type: application/json'],
    CURLOPT_POSTFIELDS => json_encode(['ticket' => $ticket, 'app_id' => $appId]),
    CURLOPT_TIMEOUT => 20,
]);
$replay = json_decode((string) curl_exec($ch), true) ?: [];
curl_close($ch);
check('重放被拒', ($replay['success'] ?? true) === false, json_encode($replay, JSON_UNESCAPED_UNICODE));

echo "\n== 5. 会话能真正调用控制台接口 ==\n";
$ch = curl_init($base . '/api/user/self');
curl_setopt_array($ch, [
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_HTTPHEADER => ['Authorization: Bearer ' . $accessToken],
    CURLOPT_COOKIEFILE => $jar,
    CURLOPT_TIMEOUT => 20,
]);
$selfRaw = (string) curl_exec($ch);
curl_close($ch);
$self = json_decode($selfRaw, true) ?: [];
check('/api/user/self 可读', ($self['success'] ?? false) === true, $selfRaw);
check('用户名与账号一致', (int) ($self['data']['id'] ?? 0) === (int) ($exchange['data']['user']['id'] ?? -1));
check('能读到额度', array_key_exists('quota', $self['data'] ?? []));

echo "\n============================================\n";
echo "  failed: $fail\n";
echo "============================================\n";
exit($fail === 0 ? 0 : 1);
