<?php
/**
 * 站点密钥轮换校验（必须最后跑）
 *
 * 轮换后旧密钥立即失效，所以这个用例会**破坏**引导密钥。
 * 只在全部其他套件跑完后执行，跑完把密钥再设回引导值，
 * 好让下次重跑不必重建数据库。
 *
 * 用法：php scripts/v10-rotate-smoke.php [base] [appId] [oldSecret] [newSecret]
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3240', '/');
$appId = $argv[2] ?? 'v10test';
$oldSecret = $argv[3] ?? 'test-secret-0123456789abcdef';
$newSecret = $argv[4] ?? 'rotated-' . bin2hex(random_bytes(8));
$adminUser = 'rootadmin';
$adminPass = 'AdminPass123x';

$ok = 0;
$bad = 0;
function check(string $name, bool $pass, string $detail = ''): void
{
    global $ok, $bad;
    if ($pass) {
        $ok++;
        echo "  PASS  $name\n";
    } else {
        $bad++;
        echo "  FAIL  $name" . ($detail !== '' ? "  -> $detail" : '') . "\n";
    }
}

$jar = tempnam(sys_get_temp_dir(), 'rot');

function http(string $method, string $url, $body = null, ?string $jar = null, ?string $token = null): array
{
    $ch = curl_init($url);
    $h = ['Content-Type: application/json'];
    if ($token !== null && $token !== '') {
        $h[] = 'Authorization: Bearer ' . $token;
    }
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CUSTOMREQUEST => $method,
        CURLOPT_HTTPHEADER => $h,
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

    return [$code, json_decode((string) $raw, true)];
}

/**
 * 用给定密钥探测一次签名接口，判断签名是否被接受。
 *
 * 注意区分两种"拒绝"：
 *   - 401 签名不对
 *   - 403 站点被停用
 * 两者都算"这次调用没通过"。只有 200（成功）与 404（签名过了但账号不存在）
 * 才说明签名本身是有效的。早期把 403 当成通过，导致停用用例假失败。
 */
function probeWithSecret(string $base, string $appId, string $secret): bool
{
    $ts = time();
    $nonce = bin2hex(random_bytes(16));
    $raw = "GET\n/internal/v10/accounts/quota\nclient_id=1\n" . hash('sha256', '') . "\n" . $ts . "\n" . $nonce;
    $sig = hash_hmac('sha256', $raw, $secret);

    $ch = curl_init($base . '/internal/v10/accounts/quota?client_id=1');
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_HTTPHEADER => [
            'X-V10-App-Id: ' . $appId,
            'X-V10-Timestamp: ' . $ts,
            'X-V10-Nonce: ' . $nonce,
            'X-V10-Signature: ' . $sig,
        ],
        CURLOPT_TIMEOUT => 20,
    ]);
    curl_exec($ch);
    $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);

    return $code === 200 || $code === 404;
}

echo "== 1. 管理员登录 ==\n";
[$c, $res] = http('POST', $base . '/api/user/login', ['username' => $adminUser, 'password' => $adminPass], $jar);
check('登录成功', ($res['success'] ?? false) === true, json_encode($res, JSON_UNESCAPED_UNICODE));

$token = (string) ($res['data']['access_token'] ?? '');
if ($token === '') {
    [$c, $r2] = http('POST', $base . '/api/user/token', null, $jar);
    $token = (string) ($r2['data'] ?? '');
}

[$c, $res] = http('GET', $base . '/api/v10/admin/sites', null, $jar, $token);
$siteId = (int) ($res['data']['items'][0]['id'] ?? 0);
check('找到引导站点', $siteId > 0, json_encode($res, JSON_UNESCAPED_UNICODE));
if ($siteId <= 0) {
    echo "\n  无法继续：没有站点\n";
    exit(1);
}

echo "\n== 2. 轮换前旧密钥可用 ==\n";
check('旧密钥通过', probeWithSecret($base, $appId, $oldSecret));

echo "\n== 3. 轮换密钥 ==\n";
[$c, $res] = http('POST', $base . '/api/v10/admin/sites/' . $siteId . '/secret', ['secret' => $newSecret], $jar, $token);
check('轮换成功', ($res['success'] ?? false) === true, json_encode($res, JSON_UNESCAPED_UNICODE));
check('返回前缀一致', ($res['data']['secret_prefix'] ?? '') === substr($newSecret, 0, 6));
check('提示需同步插件', stripos((string) ($res['data']['notice'] ?? ''), 'plugin') !== false);

echo "\n== 4. 轮换后旧密钥立即失效 ==\n";
check('旧密钥被拒', !probeWithSecret($base, $appId, $oldSecret));
check('新密钥可用', probeWithSecret($base, $appId, $newSecret));

echo "\n== 5. 复原密钥（便于重跑） ==\n";
[$c, $res] = http('POST', $base . '/api/v10/admin/sites/' . $siteId . '/secret', ['secret' => $oldSecret], $jar, $token);
check('复原成功', ($res['success'] ?? false) === true);
check('复原后旧密钥可用', probeWithSecret($base, $appId, $oldSecret));

echo "\n== 6. 停用/启用站点 ==\n";
[$c, $res] = http('POST', $base . '/api/v10/admin/sites/' . $siteId . '/status', ['enabled' => false], $jar, $token);
check('停用成功', ($res['success'] ?? false) === true);
check('停用后签名请求被拒', !probeWithSecret($base, $appId, $oldSecret));

[$c, $res] = http('POST', $base . '/api/v10/admin/sites/' . $siteId . '/status', ['enabled' => true], $jar, $token);
check('启用成功', ($res['success'] ?? false) === true);
check('启用后恢复可用', probeWithSecret($base, $appId, $oldSecret));

echo "\n============================================\n";
echo "  passed: $ok   failed: $bad\n";
echo "============================================\n";
exit($bad === 0 ? 0 : 1);
