<?php
/**
 * V10 对接层全链路验收（一次跑完全部套件）
 *
 * 顺序很重要：
 *   1. 首次初始化管理员（新库必需，幂等）
 *   2. 运维套件先跑一次，确认合规并建立套餐（host 套件依赖套餐）
 *   3. 业务套件：插件契约、host 生命周期、SSO
 *   4. 站点密钥轮换放最后：它会改写密钥，虽有复原逻辑，
 *      但一旦中途失败就会让后续用例全部 401。
 *
 * 用法：php scripts/v10-verify-all.php [base] [appId] [secret]
 */

$base = rtrim($argv[1] ?? 'http://127.0.0.1:3240', '/');
$appId = $argv[2] ?? 'v10test';
$secret = $argv[3] ?? 'test-secret-0123456789abcdef';
$adminUser = 'rootadmin';
// 密码里不能有 `!`：Windows 上 PHP 的 escapeshellarg() 会把 `!`、`%`、`"`
// 直接替换成空格，于是子进程收到的是被截断的密码，登录必然失败，
// 而错误信息只会说"用户名或密码不正确"，极难定位。
$adminPass = 'AdminPass123x';

$root = dirname(__DIR__);

function run_php(string $root, string $file, array $args): int
{
    $cmd = 'php ' . escapeshellarg($root . '/scripts/' . $file);
    foreach ($args as $arg) {
        $cmd .= ' ' . escapeshellarg((string) $arg);
    }
    $out = [];
    $code = 0;
    exec($cmd . ' 2>&1', $out, $code);
    echo implode("\n", $out) . "\n";

    return $code;
}

echo str_repeat('=', 60) . "\n";
echo "  V10 对接层验收   目标: $base\n";
echo str_repeat('=', 60) . "\n";

// ---- 步骤 0：初始化管理员 ----
echo "\n" . str_repeat('-', 60) . "\n";
echo "  >>> 0. 初始化管理员\n";
echo str_repeat('-', 60) . "\n";
$setupBody = json_encode([
    'username' => $adminUser,
    'password' => $adminPass,
    'confirmPassword' => $adminPass,
]);
$ch = curl_init($base . '/api/setup');
curl_setopt_array($ch, [
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_CUSTOMREQUEST => 'POST',
    CURLOPT_HTTPHEADER => ['Content-Type: application/json'],
    CURLOPT_POSTFIELDS => $setupBody,
    CURLOPT_TIMEOUT => 25,
]);
$raw = (string) curl_exec($ch);
curl_close($ch);
$setupRes = json_decode($raw, true);
echo '  ' . ($setupRes['message'] ?? $raw) . "\n";

// ---- 步骤 1：运维链路（含合规确认与建套餐）----
$suites = [
    ['运维流程（登录/合规/建套餐/站点接口）', 'v10-ops-smoke.php', [$base, $adminUser, $adminPass]],
    ['插件契约（36 项）',                     'v10-plugin-smoke.php', [$base, $appId, $secret]],
    ['host 生命周期',                         'v10-host-smoke.php',   [$base, $appId, $secret]],
    ['SSO 会话可用',                          'v10-sso-check.php',    [$base, $appId, $secret]],
    // 用户面封锁必须排在运维套件之后：它要验证的正是"合规已确认"这个
    // 真实部署状态下的行为。合规未确认时支付/兑换是被合规开关挡住的，
    // 那时候测会得出"已经关好了"的假结论。
    ['用户面封锁（V10 是唯一入口）',           'v10-surface-guard.php', [$base, $appId, $secret, $adminUser, $adminPass]],
    ['站点密钥轮换 / 停用启用',                'v10-rotate-smoke.php', [$base, $appId, $secret]],
];

// host 套件需要真实 plan_id；运维套件跑完后从库里拿。
$failed = [];
$planId = '1';

foreach ($suites as $i => [$name, $file, $args]) {
    echo "\n" . str_repeat('-', 60) . "\n";
    echo "  >>> " . ($i + 1) . ". $name\n";
    echo str_repeat('-', 60) . "\n";

    if ($file === 'v10-host-smoke.php') {
        $args[] = $planId;
    }

    $code = run_php($root, $file, $args);
    if ($code !== 0) {
        $failed[] = $name;
    }
}

echo "\n" . str_repeat('=', 60) . "\n";
if (empty($failed)) {
    echo "  全部 " . count($suites) . " 个套件通过\n";
} else {
    echo "  失败套件: " . implode('、', $failed) . "\n";
}
echo str_repeat('=', 60) . "\n";
exit(empty($failed) ? 0 : 1);
