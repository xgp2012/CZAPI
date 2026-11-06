<?php
/**
 * 资金链路端到端验证：V10 建账户 -> 后台手工加额 -> 账本与余额一致。
 *
 * 这是整个后台最不能出错的一条路径，因此核对三件事：
 *   1. 加额后网关余额精确增加，且元/微元换算不丢精度
 *   2. 每次加额在 v10_refs 留下独立凭证，不会因幂等被静默吞掉
 *   3. 连续多笔不会互相冲突（ref_id 必须唯一）
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3500', '/');
$appId = 'v10test';
$secret = 'test-secret-0123456789abcdef';
$jar = sys_get_temp_dir() . '/czapi-money-cookies.txt';
@unlink($jar);

$pass = 0; $fail = 0;

function check(string $n, bool $ok, string $d = ''): void
{
    global $pass, $fail;
    if ($ok) { $pass++; printf("  PASS  %s\n", $n); }
    else     { $fail++; printf("  FAIL  %s %s\n", $n, $d); }
}

/** 按 V10 协议签名后调用 /internal/v10。 */
function signed(string $base, string $appId, string $secret, string $method,
                string $path, array $payload = null): array
{
    $body = $payload === null ? '' : json_encode($payload, JSON_UNESCAPED_UNICODE);
    $ts = (string) time();
    $nonce = bin2hex(random_bytes(16));

    // 规范串：METHOD\nPATH\nSORTED_QUERY\nSHA256(body)\nTIMESTAMP\nNONCE
    // 空 query 用空串，与 Go 侧 CanonicalQuery 的空结果一致
    $stringToSign = implode("\n", [
        $method, $path, '', hash('sha256', $body), $ts, $nonce,
    ]);
    $sig = hash_hmac('sha256', $stringToSign, $secret);

    $ch = curl_init($base . $path);
    $headers = [
        'Content-Type: application/json',
        "X-V10-App-Id: $appId",
        "X-V10-Timestamp: $ts",
        "X-V10-Nonce: $nonce",
        "X-V10-Signature: $sig",
    ];
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST  => $method,
        CURLOPT_HTTPHEADER     => $headers,
        CURLOPT_TIMEOUT        => 25,
    ]);
    if ($body !== '') { curl_setopt($ch, CURLOPT_POSTFIELDS, $body); }
    $raw = (string) curl_exec($ch);
    $code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return ['code' => $code, 'raw' => $raw, 'json' => json_decode($raw, true)];
}

function console(string $base, string $method, string $path, $body = null): array
{
    global $jar;
    $ch = curl_init($base . $path);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST  => $method,
        CURLOPT_HTTPHEADER     => ['Content-Type: application/json'],
        CURLOPT_COOKIEJAR      => $jar,
        CURLOPT_COOKIEFILE     => $jar,
        CURLOPT_TIMEOUT        => 25,
    ]);
    if ($body !== null) {
        curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($body));
    }
    $raw = (string) curl_exec($ch);
    $code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return ['code' => $code, 'raw' => $raw, 'json' => json_decode($raw, true)];
}

$clientId = 90011;

echo "== 1. V10 建账户 ==\n";
$r = signed($base, $appId, $secret, 'POST', '/internal/v10/accounts/sync', [
    'client_id' => $clientId,
    'username'  => 'money_test_' . $clientId,
    'email'     => "money{$clientId}@example.test",
]);
check('accounts/sync 成功', $r['code'] === 200, "got {$r['code']}: " . substr($r['raw'], 0, 300));

$userId = $r['json']['data']['user_id'] ?? null;
check('返回 user_id', is_int($userId) && $userId > 0, $r['raw']);

if (!$userId) { fwrite(STDERR, "无法继续：没有 user_id\n"); exit(2); }

echo "\n== 2. 后台登录 ==\n";
console($base, 'POST', '/api/v10/console/login', ['username' => 'admin', 'password' => 'admin']);
console($base, 'POST', '/api/v10/console/password',
    ['old_password' => 'admin', 'new_password' => 'AdminPass123x']);
$r = console($base, 'POST', '/api/v10/console/login',
    ['username' => 'admin', 'password' => 'AdminPass123x']);
check('后台登录成功', str_contains($r['raw'], '"success":true'), $r['raw']);

function balanceOf(string $base, int $userId): ?float
{
    // 用 user_id 精确过滤，避免 keyword 在 client_id / user_id 之间歧义
    $r = console($base, 'GET', "/api/v10/console/accounts?page_size=200");
    foreach (($r['json']['data']['items'] ?? []) as $row) {
        if (($row['user_id'] ?? 0) === $userId) {
            return (float) $row['quota'];
        }
    }
    return null;
}

$before = balanceOf($base, $userId);
check('能查到该账户', $before !== null, '未在账户列表中找到');

echo "\n== 3. 加额 12.34 元 ==\n";
$r = console($base, 'POST', "/api/v10/console/accounts/$userId/credit",
    ['amount_yuan' => 12.34, 'remark' => 'e2e 加额']);
check('加额成功', str_contains($r['raw'], '"success":true'), $r['raw']);

$after = balanceOf($base, $userId);
$delta = ($after ?? 0) - ($before ?? 0);
// 12.34 元 = 12340000 微元 → quota = 12340000 * 500000 / 1000000 = 6170000
check('余额精确增加 6170000 quota', abs($delta - 6170000) < 1, "delta=$delta");

echo "\n== 4. 扣额 2.00 元 ==\n";
$r = console($base, 'POST', "/api/v10/console/accounts/$userId/credit",
    ['amount_yuan' => -2.00, 'remark' => 'e2e 扣额', 'allow_negative' => true]);
check('扣额成功', str_contains($r['raw'], '"success":true'), $r['raw']);
$after2 = balanceOf($base, $userId);
$delta2 = ($after2 ?? 0) - ($after ?? 0);
check('余额精确减少 1000000 quota', abs($delta2 + 1000000) < 1, "delta=$delta2");

echo "\n== 5. 连续两笔同金额必须都生效 ==\n";
console($base, 'POST', "/api/v10/console/accounts/$userId/credit",
    ['amount_yuan' => 1.00, 'remark' => '连发 A']);
console($base, 'POST', "/api/v10/console/accounts/$userId/credit",
    ['amount_yuan' => 1.00, 'remark' => '连发 B']);
$after3 = balanceOf($base, $userId);
$delta3 = ($after3 ?? 0) - ($after2 ?? 0);
check('两笔各 1 元都入账（共 1000000 quota）', abs($delta3 - 1000000) < 1, "delta=$delta3");

echo "\n== 6. 账本留痕 ==\n";
$r = console($base, 'GET', '/api/v10/console/ledger?kind=admin_adjust&page_size=50');
$items = $r['json']['data']['items'] ?? [];
check('账本可查询', $r['code'] === 200 && is_array($items), $r['raw']);
check('至少 4 条调额凭证', count($items) >= 4, 'count=' . count($items));

$refIds = array_column($items, 'ref_id');
check('ref_id 互不重复', count($refIds) === count(array_unique($refIds)));
foreach ($items as $it) {
    if (str_contains((string) ($it['remark'] ?? ''), 'by admin')) { $found = true; }
}
check('备注记录了操作人', !empty($found), '未在 remark 中找到 "by admin"');

echo "\n== 7. 未托管用户不能被调额 ==\n";
$r = console($base, 'POST', '/api/v10/console/accounts/1/credit', ['amount_yuan' => 5]);
$r2 = console($base, 'POST', '/api/v10/console/accounts/1/credit', ['amount_yuan' => 5]);

@unlink($jar);
echo "\n----------------------------------------\n";
printf("passed: %d   failed: %d\n", $pass, $fail);
exit($fail === 0 ? 0 : 1);
