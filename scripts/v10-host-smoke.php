<?php
/**
 * host 生命周期校验：绑定 -> 暂停（连带封 Key）-> 恢复 -> 终止
 *
 * 这条链路是"用户购买套餐"的核心：V10 订单驱动 host 开通，
 * host 状态又驱动 Key 可用性。任何一环错都能让客户白花钱或欠费用量。
 *
 * 用法：php scripts/v10-host-smoke.php [base] [appId] [secret] [planId]
 */

require __DIR__ . '/../v10-plugin/idcsmart_llm_gateway/model/LlmApi.php';

if (!class_exists('think\Model')) {
    eval('namespace think; class Model implements \ArrayAccess {
        public static array $rows = [];
        public static int $seq = 0;
        public array $data = [];
        public function __construct(array $d = []) { $this->data = $d; }
        public static function create(array $d) { return new static($d); }
        public static function find($id) { return null; }
        public static function where(...$a) { return new static(); }
        public function save(array $d) { return true; }
        public function update(...$a) { return 1; }
        public function __get($k) { return $this->data[$k] ?? null; }
        public function __set($k, $v) { $this->data[$k] = $v; }
        public function offsetExists($o): bool { return array_key_exists($o, $this->data); }
        public function offsetGet($o): mixed { return $this->data[$o] ?? null; }
        public function offsetSet($o, $v): void { $this->data[$o] = $v; }
        public function offsetUnset($o): void { unset($this->data[$o]); }
        public function toArray() { return $this->data; }
    }');
}

spl_autoload_register(function ($class) {
    $prefix = 'addon\\idcsmart_llm_gateway\\model\\';
    if (!str_starts_with($class, $prefix)) {
        return;
    }
    $path = __DIR__ . '/../v10-plugin/idcsmart_llm_gateway/model/' . substr($class, strlen($prefix)) . '.php';
    if (is_file($path)) {
        require_once $path;
    }
});

$base = $argv[1] ?? 'http://127.0.0.1:3240';
$appId = $argv[2] ?? 'v10test';
$secret = $argv[3] ?? 'test-secret-0123456789abcdef';
$planId = (int) ($argv[4] ?? 1);

if (!class_exists('addon\idcsmart_llm_gateway\logic\LlmGatewayLogic')) {
    eval('namespace addon\idcsmart_llm_gateway\logic; class LlmGatewayLogic {
        public static array $stub = [];
        public static function config($n = null, $d = null) { return self::$stub[$n] ?? $d; }
        public static function configInt($n, $d = 0) { return (int) (self::$stub[$n] ?? $d); }
        public static function audit($d, $o = 0) {}
    }');
}

$logic = 'addon\idcsmart_llm_gateway\logic\LlmGatewayLogic';
$logic::$stub = [
    'gateway_base_url' => $base,
    'app_id' => $appId,
    'app_secret' => $secret,
    'timeout_ms' => 15000,
];

$api = 'addon\idcsmart_llm_gateway\model\LlmApi';

$pass = 0;
$fail = 0;
function check(string $name, bool $ok, string $detail = ''): void
{
    global $pass, $fail;
    if ($ok) {
        $pass++;
        echo "  PASS  $name\n";
    } else {
        $fail++;
        echo "  FAIL  $name" . ($detail !== '' ? "  -> $detail" : '') . "\n";
    }
}

$clientId = random_int(100000, 999999);
$hostId = random_int(1000000, 9999999);

echo "== 0. 准备账户 ==\n";
$res = $api::syncAccount($clientId, 'host测试客户', 'host@v10.local');
check('账户就绪', (int) $res['code'] === 0, (string) $res['msg']);

echo "\n== 1. 绑定 host（开通套餐） ==\n";
$res = $api::bindHost([
    'client_id' => $clientId,
    'host_id' => $hostId,
    'plan_id' => $planId,
    'duration_days' => 30,
    'ref_type' => 'v10_host_open',
    'ref_id' => 'OPEN-' . $hostId,
]);
check('绑定成功', (int) $res['code'] === 0, (string) $res['msg'] . ' / planId=' . $planId);
$sub = $res['data'] ?? [];
check('返回 host_status=active', ($sub['host_status'] ?? '') === 'active', json_encode($sub, JSON_UNESCAPED_UNICODE));
$firstEnd = (int) ($sub['end_time'] ?? 0);
check('返回 end_time', $firstEnd > time());

echo "\n== 2. 建 Key 并确认可用 ==\n";
$res = $api::createKey(['client_id' => $clientId, 'host_id' => $hostId, 'name' => 'host测试Key']);
check('建 Key 成功', (int) $res['code'] === 0, (string) $res['msg']);
$keyId = (int) ($res['data']['key_id'] ?? 0);
check('Key 初始 active', ($res['data']['status'] ?? '') === 'active');

echo "\n== 3. 重复绑定 = 续期，不是新开 ==\n";
$res = $api::bindHost([
    'client_id' => $clientId,
    'host_id' => $hostId,
    'plan_id' => $planId,
    'duration_days' => 30,
    'ref_type' => 'v10_host_renew',
    'ref_id' => 'RENEW-' . $hostId . '-1',
]);
check('续期成功', (int) $res['code'] === 0, (string) $res['msg']);
$secondEnd = (int) ($res['data']['end_time'] ?? 0);
check('到期时间被延后', $secondEnd > $firstEnd, "before=$firstEnd after=$secondEnd");

echo "\n== 4. 暂停：连带封禁 Key ==\n";
$res = $api::setHostStatus($hostId, 'suspended', '欠费');
check('暂停成功', (int) $res['code'] === 0, (string) $res['msg']);

$res = $api::listKeys($clientId, $hostId);
$keyView = null;
foreach (($res['data']['items'] ?? []) as $item) {
    if ((int) $item['key_id'] === $keyId) {
        $keyView = $item;
    }
}
check('Key 被置为 suspended', ($keyView['status'] ?? '') === 'suspended',
    'got ' . ($keyView['status'] ?? 'none'));

echo "\n== 5. 恢复：Key 回到 active ==\n";
$res = $api::setHostStatus($hostId, 'active', 'V10 恢复');
check('恢复成功', (int) $res['code'] === 0, (string) $res['msg']);

$res = $api::listKeys($clientId, $hostId);
$keyView = null;
foreach (($res['data']['items'] ?? []) as $item) {
    if ((int) $item['key_id'] === $keyId) {
        $keyView = $item;
    }
}
check('Key 回到 active', ($keyView['status'] ?? '') === 'active', 'got ' . ($keyView['status'] ?? 'none'));

echo "\n== 6. 终止：Key 全部吊销 ==\n";
$res = $api::setHostStatus($hostId, 'terminated', 'V10 产品删除');
check('终止成功', (int) $res['code'] === 0, (string) $res['msg']);

$res = $api::listKeys($clientId, $hostId);
$keyView = null;
foreach (($res['data']['items'] ?? []) as $item) {
    if ((int) $item['key_id'] === $keyId) {
        $keyView = $item;
    }
}
check('Key 已 revoked', ($keyView['status'] ?? '') === 'revoked', 'got ' . ($keyView['status'] ?? 'none'));

echo "\n== 7. 幂等：重复终止不报错 ==\n";
$res = $api::setHostStatus($hostId, 'terminated', 'V10 产品删除');
check('重复终止幂等', (int) $res['code'] === 0, 'code=' . $res['code'] . ' ' . $res['msg']);

echo "\n== 8. 参数校验 ==\n";
$res = $api::bindHost([
    'client_id' => $clientId, 'host_id' => $hostId, 'plan_id' => 0,
    'ref_type' => 'v10_host_open', 'ref_id' => 'X',
]);
check('plan_id=0 被拒', (int) $res['code'] !== 0, 'code=' . $res['code']);

echo "\n============================================\n";
echo "  passed: $pass   failed: $fail\n";
echo "============================================\n";
exit($fail === 0 ? 0 : 1);
