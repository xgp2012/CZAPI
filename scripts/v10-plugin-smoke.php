<?php
/**
 * V10 插件 -> 网关 全链路契约校验
 *
 * 目的：在不安装 V10 的前提下，证明插件里的 LlmApi 与网关 /internal/v10
 * 的每一个接口都能对上——签名、字段名、金额单位、状态流转。
 *
 * 用法：php scripts/v10-plugin-smoke.php [base] [appId] [secret]
 */

require __DIR__ . '/../v10-plugin/idcsmart_llm_gateway/model/LlmApi.php';

// LlmApi 会用到同目录的 PushLogModel / TxnModel / KeyModel / UsageModel，
// 它们继承 think\Model，脱离 V10 框架无法实例化；这里注入同名替身，
// 让 LlmApi 的真实签名、字段组装、错误分类逻辑原样跑起来。
if (!class_exists('think\Model')) {
    eval('namespace think; class Model implements \ArrayAccess {
        public static array $rows = [];
        public static int $seq = 0;
        public array $data = [];
        public function __construct(array $d = []) { $this->data = $d; }
        public static function create(array $d) {
            $d["id"] = ++self::$seq;
            self::$rows[] = $d;
            return new static($d);
        }
        public static function find($id) { return null; }
        public static function where(...$a) { return new static(); }
        public function save(array $d) { return true; }
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
    $name = substr($class, strlen($prefix));
    $path = __DIR__ . '/../v10-plugin/idcsmart_llm_gateway/model/' . $name . '.php';
    if (is_file($path)) {
        require_once $path;
    }
});

$base = $argv[1] ?? 'http://127.0.0.1:3240';
$appId = $argv[2] ?? 'v10test';
$secret = $argv[3] ?? 'test-secret-0123456789abcdef';

// 插件里 LlmApi 依赖 LlmGatewayLogic::config 取网关地址与密钥，
// 这里用同名静态类伪造一个配置源，让真实签名代码原样跑起来。
if (!class_exists('addon\idcsmart_llm_gateway\logic\LlmGatewayLogic')) {
    eval('namespace addon\idcsmart_llm_gateway\logic; class LlmGatewayLogic {
        public static array $stub = [];
        public static function config($name = null, $default = null) {
            return self::$stub[$name] ?? $default;
        }
        public static function configInt($name, $default = 0) {
            return (int) (self::$stub[$name] ?? $default);
        }
        public static function audit($desc, $objectId = 0) {}
        public static function toMicros($yuan) {
            return (int) round(((float) $yuan) * 1000000);
        }
        public static function fromMicros($micros) {
            return number_format(((int) $micros) / 1000000, 6, ".", "");
        }
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

// 每次运行用独立 client_id：网关用户表是持久的，固定 id 会让第二次运行的
// /"账户同步" 命中旧账户，进而让"入账增量"之类的断言失真。
$clientId = random_int(100000, 999999);

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

echo "== 1. 健康探测（免签名接口） ==\n";
$res = $api::health();
check('health code=0', (int) $res['code'] === 0, json_encode($res, JSON_UNESCAPED_UNICODE));

echo "\n== 2. 账户同步 ==\n";
$res = $api::syncAccount($clientId, '契约测试客户', 'contract@v10.local');
check('accounts/sync 成功', (int) $res['code'] === 0, (string) $res['msg']);
check('返回 user_id', (int) ($res['data']['user_id'] ?? 0) > 0);

$before = $api::quota($clientId);
$beforeMicros = (int) ($before['data']['balance_micros'] ?? 0);

echo "\n== 3. 加额度（幂等） ==\n";
$refId = 'CONTRACT-' . bin2hex(random_bytes(4));
$res = $api::creditAccount($clientId, 'v10_order', 2_500_000, 'v10_order', $refId, '契约测试充值');
check('credit 成功', (int) $res['code'] === 0, (string) $res['msg']);
$firstBalance = (int) ($res['data']['balance_after'] ?? 0);
check('首次 applied=true', ($res['data']['applied'] ?? null) === true);
// 2.5 元必须换算成 2.5 * QuotaPerUnit，而不是被截断成 2 元。
// 断言增量而不是绝对值：这个账户可能被重复跑过，绝对值会假失败。
$deltaQuota = $firstBalance - (int) round($beforeMicros * 500000 / 1000000);
check('入账额度未被截断（2.5 元）', $deltaQuota === (int) (2.5 * 500000),
    "delta=$deltaQuota before=$beforeMicros after=$firstBalance");

$replay = $api::creditAccount($clientId, 'v10_order', 2_500_000, 'v10_order', $refId, '契约测试充值');
check('重放 applied=false', ($replay['data']['applied'] ?? null) === false);
check('重放后余额不变', (int) ($replay['data']['balance_after'] ?? -1) === $firstBalance);

echo "\n== 4. 余额查询 ==\n";
$res = $api::quota($clientId);
check('accounts/quota 成功', (int) $res['code'] === 0, (string) $res['msg']);
check('balance_micros 与加额一致', (int) ($res['data']['balance_micros'] ?? 0) === $beforeMicros + 2_500_000,
    'got ' . (int) ($res['data']['balance_micros'] ?? 0) . ' expected ' . ($beforeMicros + 2_500_000));

echo "\n== 5. 模型目录 ==\n";
$res = $api::models();
check('models 成功', (int) $res['code'] === 0, (string) $res['msg']);
check('返回 items 数组', is_array($res['data']['items'] ?? null));

echo "\n== 6. Key 创建 / 列表 / 改名 / 吊销 ==\n";
$res = $api::createKey(['client_id' => $clientId, 'name' => '契约测试 Key', 'models' => ['gpt-4o-mini']]);
check('创建 Key 成功', (int) $res['code'] === 0, (string) $res['msg']);
$keyId = (int) ($res['data']['key_id'] ?? 0);
$plain = (string) ($res['data']['key'] ?? '');
check('返回明文 Key', $plain !== '');
check('明文以 sk- 开头', str_starts_with($plain, 'sk-'));

$res = $api::listKeys($clientId);
check('列表成功', (int) $res['code'] === 0, (string) $res['msg']);
$listed = $res['data']['items'] ?? [];
check('列表含刚建的 Key', count(array_filter($listed, fn($i) => (int) $i['key_id'] === $keyId)) === 1);
$leak = false;
foreach ($listed as $item) {
    if (($item['key'] ?? '') !== '') {
        $leak = true;
    }
}
check('列表不泄露明文', !$leak);

$res = $api::updateKey($clientId, $keyId, ['name' => '改名后的 Key']);
check('改名成功', (int) $res['code'] === 0, (string) $res['msg']);

$res = $api::deleteKey($clientId, $keyId);
check('吊销成功', (int) $res['code'] === 0, (string) $res['msg']);
$res = $api::deleteKey($clientId, $keyId);
check('重复吊销幂等', (int) $res['code'] === 0, (string) $res['msg']);

echo "\n== 7. 用量（空数据也要结构正确） ==\n";
$res = $api::usageSummary(['client_id' => $clientId, 'group_by' => 'day']);
check('usage/summary 成功', (int) $res['code'] === 0, (string) $res['msg']);
check('totals 有 quota_micros', array_key_exists('quota_micros', $res['data']['totals'] ?? []));
check('buckets 是数组', is_array($res['data']['buckets'] ?? null));

$res = $api::usageLogs(['client_id' => $clientId, 'page' => 1, 'page_size' => 10]);
check('usage/logs 成功', (int) $res['code'] === 0, (string) $res['msg']);
check('logs 有 total', array_key_exists('total', $res['data'] ?? []));

echo "\n== 8. SSO 票据 ==\n";
$res = $api::ssoTicket($clientId);
check('sso/ticket 成功', (int) $res['code'] === 0, (string) $res['msg']);
$loginUrl = (string) ($res['data']['login_url'] ?? '');
check('返回 login_url', $loginUrl !== '');
check('票据在 fragment（不进日志）', str_contains($loginUrl, '/v10/login#'));

echo "\n== 9. 错误处理 ==\n";
$res = $api::credit(['client_id' => $clientId, 'amount_micros' => 100]);
check('缺 ref_type/ref_id 本地拦截', (int) $res['code'] === 40001, (string) $res['msg']);

$logic::$stub['app_secret'] = 'wrong-secret';
$res = $api::quota($clientId);
check('错误密钥被拒', (int) $res['code'] === 40100 || (int) $res['http_status'] === 401,
    'code=' . $res['code'] . ' http=' . $res['http_status']);
$logic::$stub['app_secret'] = $secret;

echo "\n== 10. 幂等分类 ==\n";
check('50300 -> stop', $api::classify(['code' => 50300, 'http_status' => 503]) === 'stop');
check('50000 -> retry', $api::classify(['code' => 50000, 'http_status' => 500]) === 'retry');
check('-1 网络 -> retry', $api::classify(['code' => -1, 'http_status' => 0]) === 'retry');
check('40001 -> done', $api::classify(['code' => 40001, 'http_status' => 400]) === 'done');
check('40102 -> done', $api::classify(['code' => 40102, 'http_status' => 401]) === 'done');

echo "\n============================================\n";
echo "  passed: $pass   failed: $fail\n";
echo "============================================\n";
exit($fail === 0 ? 0 : 1);
