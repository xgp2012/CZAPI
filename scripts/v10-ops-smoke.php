<?php
/**
 * 运维流程校验：管理员登录 -> 建套餐 -> 建站点 -> 插件可用
 *
 * 这是上线时真正要走的顺序。任何一步卡住，客户都买不到东西。
 * 用法：php scripts/v10-ops-smoke.php [base] [user] [pass]
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3240', '/');
$user = $argv[2] ?? 'rootadmin';
$pass = $argv[3] ?? 'AdminPass123x';

$pass_ = 0;
$fail = 0;
function check(string $name, bool $ok, string $detail = ''): void
{
    global $pass_, $fail;
    if ($ok) {
        $pass_++;
        echo "  PASS  $name\n";
    } else {
        $fail++;
        echo "  FAIL  $name" . ($detail !== '' ? "  -> $detail" : '') . "\n";
    }
}

$jar = tempnam(sys_get_temp_dir(), 'ops');

function http(string $method, string $url, $body = null, ?string $jar = null, ?string $token = null): array
{
    $ch = curl_init($url);
    $headers = ['Content-Type: application/json'];
    if ($token !== null) {
        $headers[] = 'Authorization: Bearer ' . $token;
    }
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST => $method,
        CURLOPT_HTTPHEADER => $headers,
        CURLOPT_TIMEOUT => 25,
    ]);
    if ($jar !== null) {
        curl_setopt($ch, CURLOPT_COOKIEJAR, $jar);
        curl_setopt($ch, CURLOPT_COOKIEFILE, $jar);
    }
    if ($body !== null) {
        curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($body, JSON_UNESCAPED_UNICODE));
    }
    $raw = curl_exec($ch);
    $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);

    return [$code, json_decode((string) $raw, true), $raw];
}

echo "== 1. 管理员密码登录（V10 客户不该能用，运维必须能用） ==\n";
[$code, $res] = http('POST', $base . '/api/user/login', ['username' => $user, 'password' => $pass], $jar);
check('管理员登录成功', ($res['success'] ?? false) === true || ($res['data']['require_2fa'] ?? false) === true,
    json_encode($res, JSON_UNESCAPED_UNICODE));

$token = '';
// 有些版本 login 直接返回 access_token；没有的话用 session 走 refresh 换一次。
$token = (string) ($res['data']['access_token'] ?? '');
if ($token === '') {
    [$c2, $r2] = http('POST', $base . '/api/user/token', null, $jar);
    $token = (string) ($r2['data'] ?? '');
}
check('拿到管理员 token', $token !== '', 'token 为空');

echo "\n== 2. 确认付款合规（建套餐的前置门禁） ==\n";
// new-api 要求管理员先显式确认合规条款才允许创建套餐/充值类配置。
// 这是上游的安全设计，不去绕过它，而是把它纳入正常上线流程。
[$code, $res] = http('POST', $base . '/api/option/payment_compliance', ['confirmed' => true], $jar, $token);
check('合规确认成功', ($res['success'] ?? false) === true, json_encode($res, JSON_UNESCAPED_UNICODE));

echo "\n== 3. 创建 V10 套餐（客户买的就是这一档） ==\n";
$planTitle = 'V10 测试套餐 ' . date('His');
[$code, $res] = http('POST', $base . '/api/subscription/admin/plans', [
    'plan' => [
        'title' => $planTitle,
        'price_amount' => 99,
        'currency' => 'USD',
        'duration_unit' => 'month',
        'duration_value' => 1,
        'total_amount' => 50000000,
        'enabled' => true,
    ],
], $jar, $token);
check('建套餐成功', ($res['success'] ?? false) === true, json_encode($res, JSON_UNESCAPED_UNICODE));
$planId = (int) ($res['data']['id'] ?? ($res['data']['plan']['id'] ?? 0));
check('拿到 plan_id', $planId > 0, 'id=' . $planId);

echo "\n== 4. 站点管理接口可用 ==\n";
[$code, $res] = http('GET', $base . '/api/v10/admin/site-options', null, $jar, $token);
check('site-options 可读', ($res['success'] ?? false) === true, json_encode($res, JSON_UNESCAPED_UNICODE));
$data = $res['data'] ?? [];
check('返回 sites 列表', is_array($data['sites'] ?? null));
check('返回 plans 列表', is_array($data['plans'] ?? null));
check('plans 含刚建的套餐', count(array_filter($data['plans'] ?? [], fn($p) => (int) $p['id'] === $planId)) === 1);
check('返回 quota_per_unit', (int) ($data['bridge']['quota_per_unit'] ?? 0) === 500000,
    'got ' . ($data['bridge']['quota_per_unit'] ?? 'none'));

echo "\n== 5. 未登录不能碰管理接口 ==\n";
[$code, $res] = http('GET', $base . '/api/v10/admin/site-options');
check('匿名访问被拒', ($res['success'] ?? true) === false, 'code=' . $code);

echo "\n============================================\n";
echo "  passed: $pass_   failed: $fail\n";
echo "  plan_id = $planId\n";
echo "============================================\n";
exit($fail === 0 ? 0 : 1);
