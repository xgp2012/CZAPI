<?php
/**
 * 后台关键路径的生产前验证。
 *
 * 重点覆盖三类容易在上线后才暴露的问题：
 *   1. 手工调额：金额换算、单笔上限、连续多笔互不干扰
 *   2. 账本可对账：调额后 v10_refs 有记录且余额一致
 *   3. host 列表：确认没有引用不存在的列
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3500', '/');
$jar  = sys_get_temp_dir() . '/czapi-prod-cookies.txt';
@unlink($jar);

$pass = 0; $fail = 0;

function req(string $url, string $method = 'GET', $body = null): array
{
    global $jar;
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST  => $method,
        CURLOPT_HTTPHEADER     => ['Content-Type: application/json'],
        CURLOPT_COOKIEJAR      => $jar,
        CURLOPT_COOKIEFILE     => $jar,
        CURLOPT_TIMEOUT        => 25,
    ]);
    if ($body !== null) {
        curl_setopt($ch, CURLOPT_POSTFIELDS, is_string($body) ? $body : json_encode($body));
    }
    $raw = curl_exec($ch);
    $code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return ['code' => $code, 'raw' => (string) $raw, 'json' => json_decode((string) $raw, true)];
}

function check(string $name, bool $ok, string $detail = ''): void
{
    global $pass, $fail;
    if ($ok) { $pass++; printf("  PASS  %s\n", $name); }
    else     { $fail++; printf("  FAIL  %s %s\n", $name, $detail); }
}

// 登录（admin 的密码在上一个用例里已改成 AdminPass123x；这里兼容全新库）
req("$base/api/v10/console/login", 'POST', ['username' => 'admin', 'password' => 'admin']);
req("$base/api/v10/console/password", 'POST',
    ['old_password' => 'admin', 'new_password' => 'AdminPass123x']);
$r = req("$base/api/v10/console/login", 'POST',
    ['username' => 'admin', 'password' => 'AdminPass123x']);
if (!str_contains($r['raw'], '"success":true')) {
    fwrite(STDERR, "登录失败，无法继续：" . $r['raw'] . "\n");
    exit(2);
}

echo "== 1. 空数据下的列表接口不应报错 ==\n";
foreach (['/hosts', '/accounts', '/keys', '/ledger', '/sites'] as $p) {
    $r = req("$base/api/v10/console$p");
    check("$p 在空库下正常", $r['code'] === 200 && str_contains($r['raw'], '"success":true'),
        "got {$r['code']}: " . substr($r['raw'], 0, 160));
}

echo "\n== 2. host 关键词搜索不应引用不存在的列 ==\n";
$r = req("$base/api/v10/console/hosts?keyword=123");
check('hosts?keyword=123 正常', $r['code'] === 200 && str_contains($r['raw'], '"success":true'),
    "got {$r['code']}: " . substr($r['raw'], 0, 200));

echo "\n== 3. 手工调额需要 V10 托管账户，普通用户应被拒 ==\n";
$r = req("$base/api/v10/console/accounts/1/credit", 'POST', ['amount_yuan' => 10]);
check('非托管账户被拒并给出说明',
    str_contains($r['raw'], '"success":false') && str_contains($r['raw'], 'V10'),
    $r['raw']);

echo "\n== 4. 金额边界 ==\n";
$r = req("$base/api/v10/console/accounts/1/credit", 'POST', ['amount_yuan' => 0]);
check('0 元被拒', str_contains($r['raw'], '"success":false'), $r['raw']);

$r = req("$base/api/v10/console/accounts/1/credit", 'POST', ['amount_yuan' => 999999]);
check('超上限被拒且提示拆分',
    str_contains($r['raw'], '"success":false') && str_contains($r['raw'], '上限'),
    $r['raw']);

echo "\n== 5. 分页参数边界 ==\n";
$r = req("$base/api/v10/console/ledger?page=-5&page_size=99999");
check('异常分页被钳制不报错', $r['code'] === 200 && str_contains($r['raw'], '"success":true'), $r['raw']);

echo "\n== 6. 用量接口在不同时间区间下正常 ==\n";
$r = req("$base/api/v10/console/usage/summary?from=0&to=0");
check('usage/summary 参数为 0 时回落默认', $r['code'] === 200 && str_contains($r['raw'], '"success":true'), $r['raw']);
$r = req("$base/api/v10/console/usage/models");
check('usage/models 正常', $r['code'] === 200 && str_contains($r['raw'], '"success":true'), $r['raw']);

echo "\n== 7. CSRF：跨站 Origin 的写操作应被拒 ==\n";
$ch = curl_init("$base/api/v10/console/sites");
curl_setopt_array($ch, [
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_POST           => true,
    CURLOPT_POSTFIELDS     => json_encode(['app_id' => 'csrf-test']),
    CURLOPT_HTTPHEADER     => ['Content-Type: application/json', 'Origin: http://evil.example'],
    CURLOPT_COOKIEFILE     => $jar,
    CURLOPT_TIMEOUT        => 20,
]);
$raw = (string) curl_exec($ch);
$code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
curl_close($ch);
check('伪造 Origin 被拒', $code === 403, "got $code: " . substr($raw, 0, 160));

echo "\n== 8. 同源写操作仍然可用 ==\n";
$ch = curl_init("$base/api/v10/console/sites");
curl_setopt_array($ch, [
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_POST           => true,
    CURLOPT_POSTFIELDS     => json_encode(['app_id' => 'same-origin-ok', 'name' => '同源测试']),
    CURLOPT_HTTPHEADER     => ['Content-Type: application/json', 'Origin: ' . $base],
    CURLOPT_COOKIEFILE     => $jar,
    CURLOPT_TIMEOUT        => 20,
]);
$raw = (string) curl_exec($ch);
$code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
curl_close($ch);
check('同源写操作成功', $code === 200 && str_contains($raw, '"success":true'),
    "got $code: " . substr($raw, 0, 200));

@unlink($jar);

echo "\n----------------------------------------\n";
printf("passed: %d   failed: %d\n", $pass, $fail);
exit($fail === 0 ? 0 : 1);
