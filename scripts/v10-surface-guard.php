<?php
/**
 * 套件：用户面封锁 —— 验证"V10 是唯一入口"这条边界真的守住了。
 *
 * 为什么必须有这个套件：
 *   开关值（register_enabled 等）只管前端渲染，不管后端接口是否放行。
 *   new-api 的 /api/user/topup 实际是**卡密兑换**，只由"合规已确认"这一个
 *   条件把守；而运维为了建订阅套餐**必须**先确认合规。也就是说，在真实
 *   部署状态下，这条路径对任何登录用户都是开着的。
 *
 *   开发期实测过：V10 客户拿 SSO 会话，用一张真实网关卡密成功给自己
 *   加了 500000 额度，完全绕过 V10 的订单与对账。因此这里必须用**真实
 *   卡密**去试，不能用假卡密——假卡密在任何实现下都会失败，那样的断言
 *   只是假保障。
 *
 * 用法：php scripts/v10-surface-guard.php [base] [appId] [secret] [adminUser] [adminPass]
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3240', '/');
$appId = $argv[2] ?? 'v10test';
$secret = $argv[3] ?? 'test-secret-0123456789abcdef';
$adminUser = $argv[4] ?? 'rootadmin';
$adminPass = $argv[5] ?? 'AdminPass123x';

$pass = 0;
$fail = 0;

function check(string $label, bool $ok, string $detail = ''): void
{
    global $pass, $fail;
    if ($ok) {
        $pass++;
        echo "  PASS  $label\n";
    } else {
        $fail++;
        echo "  FAIL  $label" . ($detail !== '' ? "  ($detail)" : '') . "\n";
    }
}

function http(string $method, string $path, $body = null, ?string $token = null): array
{
    $ch = curl_init($GLOBALS['base'] . $path);
    $headers = ['Content-Type: application/json'];
    if ($token !== null) {
        $headers[] = 'Authorization: Bearer ' . $token;
    }
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST  => strtoupper($method),
        CURLOPT_HTTPHEADER     => $headers,
        CURLOPT_TIMEOUT        => 20,
    ]);
    if ($body !== null) {
        curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($body, JSON_UNESCAPED_UNICODE));
    }
    $raw = curl_exec($ch);
    $code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return [$code, json_decode((string) $raw, true), (string) $raw];
}

/** 与插件同构的签名请求。 */
function signed(string $method, string $path, array $query, $body, string $secret): array
{
    $qs = '';
    if ($query !== []) {
        ksort($query);
        $parts = [];
        foreach ($query as $k => $v) {
            $parts[] = rawurlencode((string) $k) . '=' . rawurlencode((string) $v);
        }
        $qs = implode('&', $parts);
    }
    $payload = $body === null ? '' : json_encode($body, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
    $ts = time();
    $nonce = bin2hex(random_bytes(16));
    $toSign = implode("\n", [
        strtoupper($method), $path, $qs, hash('sha256', $payload), (string) $ts, $nonce,
    ]);
    $sig = hash_hmac('sha256', $toSign, $secret);
    $url = $GLOBALS['base'] . $path . ($qs !== '' ? '?' . $qs : '');
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST  => strtoupper($method),
        CURLOPT_HTTPHEADER     => [
            'Content-Type: application/json',
            'X-V10-App-Id: ' . $GLOBALS['appId'],
            'X-V10-Timestamp: ' . $ts,
            'X-V10-Nonce: ' . $nonce,
            'X-V10-Signature: ' . $sig,
        ],
        CURLOPT_TIMEOUT => 20,
    ]);
    if ($payload !== '') {
        curl_setopt($ch, CURLOPT_POSTFIELDS, $payload);
    }
    $raw = curl_exec($ch);
    $code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return [$code, json_decode((string) $raw, true)];
}

/** 建一个 V10 客户并换取浏览器会话。 */
function clientSession(string $secret, string $appId): array
{
    $clientId = random_int(100000, 999999);
    signed('POST', '/internal/v10/accounts/sync', [], ['client_id' => $clientId], $secret);
    [, $r] = signed('POST', '/internal/v10/sso/ticket', [], ['client_id' => $clientId], $secret);
    $ticket = $r['data']['ticket'] ?? '';
    [, $r] = http('POST', '/api/v10/sso/exchange', ['ticket' => $ticket, 'app_id' => $appId]);
    return [$clientId, (string) ($r['data']['access_token'] ?? '')];
}

echo "\n== 0. 管理员登录并确认合规（真实部署必经状态） ==\n";
[, $r] = http('POST', '/api/user/login', ['username' => $adminUser, 'password' => $adminPass]);
$adminToken = (string) ($r['data']['access_token'] ?? '');
check('管理员登录', $adminToken !== '', 'msg=' . ($r['message'] ?? '?'));

if ($adminToken === '') {
    echo "\n  无法继续：管理员登录失败\n";
    exit(1);
}

[, $r] = http('POST', '/api/option/payment_compliance', ['confirmed' => true], $adminToken);
check('合规已确认（支付/兑换开关被打开）', (int) ($r['success'] ?? 0) === 1, 'msg=' . ($r['message'] ?? '?'));

echo "\n== 1. 合规确认后，注册仍关闭 ==\n";
[, $r] = http('GET', '/api/status');
$d = $r['data'] ?? [];
check('register_enabled 为 false', empty($d['register_enabled']));
check('password_register_enabled 为 false', empty($d['password_register_enabled']));

$n = 'probe' . random_int(1000, 9999);
[, $r] = http('POST', '/api/user/register', [
    'username' => $n, 'password' => 'ProbePass123x', 'password2' => 'ProbePass123x',
]);
check('自助注册被拒', (int) ($r['success'] ?? 0) !== 1, 'msg=' . ($r['message'] ?? '?'));

echo "\n== 2. V10 客户会话 ==\n";
[$clientId, $token] = clientSession($secret, $appId);
check("客户 #$clientId 拿到会话", $token !== '');

if ($token === '') {
    echo "\n  无法继续：客户会话获取失败\n";
    exit(1);
}

echo "\n== 3. 客户不能用网关卡密给自己加额度（核心断言） ==\n";
// 用**真实**卡密：假卡密在任何实现下都会失败，那种断言证明不了拦截生效。
[, $r] = http('POST', '/api/redemption/', ['name' => '封锁测试卡密', 'quota' => 500000, 'count' => 1], $adminToken);
$realKey = (string) ($r['data'][0] ?? '');
check('管理员造出一张真实卡密', $realKey !== '', 'msg=' . ($r['message'] ?? '?'));

if ($realKey !== '') {
    [, $r] = http('POST', '/api/user/topup', ['key' => $realKey], $token);
    check('客户兑换真实卡密被拒', (int) ($r['success'] ?? 0) !== 1, 'msg=' . ($r['message'] ?? '?'));
    check(
        '拒绝原因指向业务系统而非网关故障',
        stripos((string) ($r['message'] ?? ''), 'business system') !== false,
        'msg=' . ($r['message'] ?? '?')
    );
}

echo "\n== 4. 客户看不到、也用不了充值入口 ==\n";
[, $r] = http('GET', '/api/user/topup/info', null, $token);
check('topup/info 被拒绝', (int) ($r['success'] ?? 0) !== 1, 'msg=' . ($r['message'] ?? '?'));

[, $r] = http('POST', '/api/user/pay', ['amount' => 100, 'payment_method' => 'alipay'], $token);
check('发起支付被拒', (int) ($r['success'] ?? 0) !== 1, 'msg=' . ($r['message'] ?? '?'));

[, $r] = http('POST', '/api/user/aff_transfer', ['quota' => 1000], $token);
check('邀请返利转账被拒', (int) ($r['success'] ?? 0) !== 1, 'msg=' . ($r['message'] ?? '?'));

echo "\n== 5. 客户不能自购订阅 ==\n";
// 套餐列表是**只读**信息，允许客户看到（否则他不知道自己买了什么）。
// 真正的风险在购买路径：尤其 balance/pay 会直接用网关余额扣款，
// 客户可以拿 V10 下发的额度在网关这里再买一份订阅，绕开 V10。
[, $r] = http('GET', '/api/subscription/plans', null, $token);
check('订阅套餐列表可读（只读信息，允许）', (int) ($r['success'] ?? 0) === 1, 'msg=' . ($r['message'] ?? '?'));

foreach ([
    '/api/subscription/balance/pay',
    '/api/subscription/epay/pay',
    '/api/subscription/stripe/pay',
    '/api/subscription/creem/pay',
    '/api/subscription/waffo-pancake/pay',
] as $p) {
    [, $r] = http('POST', $p, ['plan_id' => 1], $token);
    check("自购订阅 $p 被拒", (int) ($r['success'] ?? 0) !== 1, 'msg=' . ($r['message'] ?? '?'));
}

echo "\n== 6. 客户不能访问后台面 ==\n";
foreach (['/api/v10/admin/sites', '/api/redemption/', '/api/channel/?p=1', '/api/user/?p=1'] as $p) {
    [$c, $r] = http('GET', $p, null, $token);
    check("客户不能访问 $p", $c === 403 || (int) ($r['success'] ?? 0) !== 1, "http=$c");
}

echo "\n== 7. 正常功能不受影响（不能为了封锁把业务拦死） ==\n";
[$c, $r] = http('GET', '/api/user/self', null, $token);
check('客户能读自己的资料', $c === 200 && (int) ($r['success'] ?? 0) === 1, 'http=' . $c);

[, $r] = signed('GET', '/internal/v10/accounts/quota', ['client_id' => $clientId], null, $secret);
check('V10 服务端仍能查额度', (int) ($r['code'] ?? -1) === 0, 'code=' . ($r['code'] ?? '?'));

[, $r] = signed('POST', '/internal/v10/credit', [], [
    'client_id' => $clientId, 'kind' => 'v10_order', 'amount_micros' => 1000000,
    'ref_type' => 'v10_order', 'ref_id' => 'LOCK-' . $clientId,
], $secret);
check('V10 服务端仍能加额度', (int) ($r['code'] ?? -1) === 0, 'code=' . ($r['code'] ?? '?'));

echo "\n============================================\n";
echo "  passed: $pass   failed: $fail\n";
echo "============================================\n";
exit($fail === 0 ? 0 : 1);
