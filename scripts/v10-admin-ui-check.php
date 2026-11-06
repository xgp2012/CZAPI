<?php
/**
 * 管理后台端到端验证。
 *
 * 覆盖：静态页与 SPA 回落、默认密码强制改密、以及六大模块接口。
 * 用 Cookie Jar 模拟浏览器会话，验证 HttpOnly 会话真的能带上。
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3500', '/');
$jar  = sys_get_temp_dir() . '/czapi-admin-cookies.txt';
@unlink($jar);

$pass = 0; $fail = 0;

function req(string $url, string $method = 'GET', $body = null, array $headers = []): array
{
    global $jar;
    $ch = curl_init($url);
    $h = array_merge(['Content-Type: application/json'], $headers);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST  => $method,
        CURLOPT_HTTPHEADER     => $h,
        CURLOPT_COOKIEJAR      => $jar,
        CURLOPT_COOKIEFILE     => $jar,
        CURLOPT_TIMEOUT        => 25,
        CURLOPT_FOLLOWLOCATION => false,
    ]);
    if ($body !== null) {
        curl_setopt($ch, CURLOPT_POSTFIELDS, is_string($body) ? $body : json_encode($body));
    }
    $raw = curl_exec($ch);
    $code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $ctype = (string) curl_getinfo($ch, CURLINFO_CONTENT_TYPE);
    curl_close($ch);
    return ['code' => $code, 'raw' => (string) $raw, 'ctype' => $ctype];
}

function check(string $name, bool $ok, string $detail = ''): void
{
    global $pass, $fail;
    if ($ok) { $pass++; printf("  PASS  %s\n", $name); }
    else     { $fail++; printf("  FAIL  %s %s\n", $name, $detail); }
}

echo "== 1. 静态资源与 SPA 回落 ==\n";
$r = req("$base/admin/");
check('后台首页 200', $r['code'] === 200, "got {$r['code']}");
check('返回 Vue 挂载点', str_contains($r['raw'], '<div id="app">'));

foreach (['/admin/ledger', '/admin/accounts', '/admin/usage', '/admin/hosts'] as $p) {
    $r = req("$base$p");
    check("深层路由 $p 回落 index.html",
        $r['code'] === 200 && str_contains($r['raw'], '<div id="app">'),
        "got {$r['code']}");
}

$r = req("$base/admin/");
if (preg_match('#src="(/admin/[^"]+\.js)"#', $r['raw'], $m)) {
    $js = req($base . $m[1]);
    check('主 JS 可加载', $js['code'] === 200 && strlen($js['raw']) > 10000,
        "got {$js['code']}, " . strlen($js['raw']) . ' bytes');
    check('JS 内容类型正确', str_contains($js['ctype'], 'javascript'), $js['ctype']);
} else {
    check('找到 JS 引用', false);
}

echo "\n== 2. 未登录应被拒绝 ==\n";
// 鉴权失败必须返回 401，前端拦截器靠这个状态码判断"会话失效"。
$r = req("$base/api/v10/console/me");
check('未登录 /me 返回 401', $r['code'] === 401, "got {$r['code']}");
$r = req("$base/api/v10/console/sites");
check('未登录 /sites 返回 401', $r['code'] === 401, "got {$r['code']}");

echo "\n== 3. 默认账号 admin/admin 登录 ==\n";
$r = req("$base/api/v10/console/bootstrap");
check('bootstrap 探针可用', $r['code'] === 200 && str_contains($r['raw'], 'has_admin'));

$r = req("$base/api/v10/console/login", 'POST', ['username' => 'admin', 'password' => 'wrong-pass']);
check('错误密码被拒', $r['code'] !== 200 || str_contains($r['raw'], '"success":false'), $r['raw']);

$r = req("$base/api/v10/console/login", 'POST', ['username' => 'admin', 'password' => 'admin']);
$loginOk = $r['code'] === 200 && str_contains($r['raw'], '"success":true');
check('admin/admin 能登录', $loginOk, $r['raw']);
check('提示必须改密', str_contains($r['raw'], 'must_change_password":true'), $r['raw']);

echo "\n== 4. 未改密前，其它接口应被拦 ==\n";
// 改密前访问业务接口返回 403，前端据此强制跳转改密页。
$r = req("$base/api/v10/console/sites");
check('未改密访问 /sites 返回 403', $r['code'] === 403, "got {$r['code']}");
$r = req("$base/api/v10/console/me");
check('未改密仍可看 /me（豁免）', $r['code'] === 200, "got {$r['code']}");

echo "\n== 5. 改密 ==\n";
$r = req("$base/api/v10/console/password", 'POST',
    ['old_password' => 'admin', 'new_password' => 'short']);
check('弱密码被拒', str_contains($r['raw'], '"success":false'), $r['raw']);

$r = req("$base/api/v10/console/password", 'POST',
    ['old_password' => 'wrong', 'new_password' => 'AdminPass123x']);
check('原密码错误被拒', str_contains($r['raw'], '"success":false'), $r['raw']);

$r = req("$base/api/v10/console/password", 'POST',
    ['old_password' => 'admin', 'new_password' => 'AdminPass123x']);
check('改密成功', str_contains($r['raw'], '"success":true'), $r['raw']);

echo "\n== 6. 改密后各模块可用 ==\n";
foreach ([
    'overview'        => '/overview',
    'sites'           => '/sites',
    'accounts'        => '/accounts',
    'ledger'          => '/ledger',
    'keys'            => '/keys',
    'hosts'           => '/hosts',
    'usage/summary'   => '/usage/summary',
    'usage/models'    => '/usage/models',
] as $name => $path) {
    $r = req("$base/api/v10/console$path");
    $ok = $r['code'] === 200 && str_contains($r['raw'], '"success":true');
    check("$name 可访问", $ok, "got {$r['code']}: " . substr($r['raw'], 0, 120));
}

echo "\n== 7. 站点：新建 -> 轮换 -> 停用 ==\n";
$r = req("$base/api/v10/console/sites", 'POST', ['app_id' => 'uitest', 'name' => 'UI 测试站点']);
$r['code'] === 200 ? check('新建站点', str_contains($r['raw'], '"success":true'), $r['raw'])
                   : check('新建站点', false, $r['raw']);
$newSiteId = null;
if (preg_match('/"id":(\d+)/', $r['raw'], $m)) { $newSiteId = (int) $m[1]; }
check('返回了 secret（只此一次）', str_contains($r['raw'], '"secret":"'), $r['raw']);

if ($newSiteId) {
    $r = req("$base/api/v10/console/sites/$newSiteId/secret", 'POST');
    check('轮换密钥', str_contains($r['raw'], '"success":true'), $r['raw']);

    $r = req("$base/api/v10/console/sites/$newSiteId/status", 'POST', ['enabled' => false]);
    check('停用站点', str_contains($r['raw'], '"success":true'), $r['raw']);

    $r = req("$base/api/v10/console/sites/$newSiteId/status", 'POST', ['enabled' => true]);
    check('启用站点', str_contains($r['raw'], '"success":true'), $r['raw']);
}

echo "\n== 8. 登出后会话失效 ==\n";
$r = req("$base/api/v10/console/logout", 'POST');
check('登出', str_contains($r['raw'], '"success":true'), $r['raw']);
$r = req("$base/api/v10/console/sites");
check('登出后返回 401', $r['code'] === 401, "got {$r['code']}");

echo "\n== 9. 新密码可重新登录 ==\n";
$r = req("$base/api/v10/console/login", 'POST',
    ['username' => 'admin', 'password' => 'AdminPass123x']);
check('新密码登录成功', str_contains($r['raw'], '"success":true'), $r['raw']);
check('不再要求改密', str_contains($r['raw'], 'must_change_password":false'), $r['raw']);

@unlink($jar);

echo "\n----------------------------------------\n";
printf("passed: %d   failed: %d\n", $pass, $fail);
exit($fail === 0 ? 0 : 1);
