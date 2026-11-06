<?php
namespace addon\idcsmart_llm_gateway\logic;

use addon\idcsmart_llm_gateway\IdcsmartLlmGateway;
use app\admin\model\PluginModel;

/**
 * 插件通用逻辑：配置读写、套餐映射、金额换算、回调签名校验、审计日志
 *
 * @author  CZAPI
 * @version 2.0.0
 */
class LlmGatewayLogic
{
    # 配置缓存（同一次请求内只读一次 DB）
    private static $configCache = null;

    /**
     * 读取插件配置（config.php 默认值 + 后台保存值覆盖）
     *
     * 与官方 idcsmart_ticket 的 IdcsmartTicketLogic::getDefaultConfig() 同一套做法：
     *   文件默认值   = include config.php
     *   后台保存值   = (new 主类)->getConfig()  ← 由 app\common\lib\Plugin 基类提供
     *
     * @param string|null $name    配置键；为空返回整个数组
     * @param mixed       $default 键不存在时的默认值
     * @return mixed
     */
    public static function config($name = null, $default = null)
    {
        if (self::$configCache === null) {
            $fileConfig = include dirname(__DIR__) . '/config.php';
            $fileConfig = is_array($fileConfig) ? $fileConfig : [];

            $dbConfig = [];
            try {
                $dbConfig = (new IdcsmartLlmGateway())->getConfig();
                $dbConfig = is_array($dbConfig) ? $dbConfig : [];
            } catch (\Throwable $e) {
                # 插件尚未安装 / 基类不可用时，退回文件默认值
                $dbConfig = [];
            }

            $merged = [];
            foreach ($fileConfig as $key => $item) {
                $merged[$key] = is_array($item) && array_key_exists('value', $item) ? $item['value'] : $item;
            }
            foreach ($dbConfig as $key => $value) {
                $merged[$key] = $value;
            }
            self::$configCache = $merged;
        }

        if ($name === null || $name === '') {
            return self::$configCache;
        }

        return array_key_exists($name, self::$configCache) ? self::$configCache[$name] : $default;
    }

    /**
     * 读取整数型配置
     *
     * @param string $name
     * @param int    $default
     * @return int
     */
    public static function configInt($name, $default)
    {
        $value = self::config($name, $default);

        return is_numeric($value) ? (int) $value : (int) $default;
    }

    /**
     * 解析 plan_map（JSON: {"商品ID":"plan_code"}）
     *
     * @return array
     */
    public static function planMap()
    {
        $raw = self::config('plan_map', '{}');
        if (is_array($raw)) {
            return $raw;
        }

        $decoded = json_decode((string) $raw, true);

        return is_array($decoded) ? $decoded : [];
    }

    /**
     * V10 商品 ID -> 网关套餐 ID（数字）
     *
     * 网关的 host 绑定接口用的是套餐主键 id，不是编码字符串，
     * 所以映射表里存的应该是数字。历史配置里存的是 plan_code 字符串，
     * 这里只接受纯数字，别的值一律当作"未配置"，让调用方报错而不是猜一个套餐。
     *
     * @param int|string $productId
     * @return int
     */
    public static function planIdForProduct($productId)
    {
        $map = self::planMap();
        $key = (string) $productId;
        if (!isset($map[$key])) {
            return 0;
        }

        $value = $map[$key];
        if (is_int($value)) {
            return max(0, $value);
        }
        if (is_numeric($value) && (string) (int) $value === (string) $value) {
            return max(0, (int) $value);
        }

        return 0;
    }

    /**
     * 元 -> 微元（1 元 = 1_000_000 微元）
     *
     * @param float|string $yuan
     * @return int
     */
    public static function toMicros($yuan)
    {
        return (int) round(((float) $yuan) * 1000000);
    }

    /**
     * 微元 -> 元的十进制字符串（保留 6 位，避免浮点误差）
     *
     * @param int $micros
     * @return string
     */
    public static function fromMicros($micros)
    {
        return number_format(((int) $micros) / 1000000, 6, '.', '');
    }

    /**
     * 网关 -> V10 回调的签名校验（console/v1/api 免登录组专用）
     *
     * 待签串与 /internal/v10 同构（method\npath\nsha256(body)\ntimestamp\nnonce），
     * 但密钥用插件配置的 callback_secret，与 app_secret 隔离：
     * 即便 app_secret 泄露也不会被用来伪造回调。
     *
     * 常量时间比较用 hash_equals。
     *
     * // ⚠️ 待实例验证: 该回调协议是我方自定义，网关侧需同步实现；
     * //   docs/internal-api-v1.md §4 明确"本版不做反向回调"，因此该组目前是"预留 + 拒绝"状态。
     *
     * @param string $method        HTTP 方法
     * @param string $path          含前缀的请求路径，如 /console/v1/api/llm/callback/quota-exhausted
     * @param string $body          原始请求体
     * @param string $timestamp     请求头 X-CZ-Timestamp
     * @param string $nonce         请求头 X-CZ-Nonce
     * @param string $signature     请求头 X-CZ-Signature
     * @return array ['ok'=>bool,'msg'=>string]
     */
    public static function verifyCallback($method, $path, $body, $timestamp, $nonce, $signature)
    {
        $secret = (string) self::config('callback_secret', '');
        if ($secret === '') {
            return ['ok' => false, 'msg' => 'callback_secret 未配置，回调组拒绝服务'];
        }

        $timestamp = (string) $timestamp;
        $nonce = (string) $nonce;
        $signature = (string) $signature;
        if ($timestamp === '' || $nonce === '' || $signature === '') {
            return ['ok' => false, 'msg' => '缺少签名头'];
        }

        # 时钟窗 300s（与 /internal/v10 一致）
        if (abs(time() - (int) $timestamp) > 300) {
            return ['ok' => false, 'msg' => '时间戳超出窗口'];
        }

        $raw = implode("\n", [
            strtoupper((string) $method),
            (string) $path,
            hash('sha256', (string) $body),
            $timestamp,
            $nonce,
        ]);
        $expected = hash_hmac('sha256', $raw, $secret);

        if (!hash_equals($expected, strtolower($signature))) {
            return ['ok' => false, 'msg' => '签名不匹配'];
        }

        return ['ok' => true, 'msg' => 'ok'];
    }

    /**
     * 写审计日志（手机号/密码等敏感信息绝不入参）
     *
     * 与官方插件一致：active_log($描述, $标识, $对象ID)。
     * 标识固定为 addon_idcsmart_llm_gateway。
     *
     * @param string $desc
     * @param int    $objectId
     * @return void
     */
    public static function audit($desc, $objectId = 0)
    {
        try {
            active_log($desc, 'addon_idcsmart_llm_gateway', (int) $objectId);
        } catch (\Throwable $e) {
            # 审计失败不得影响主流程
        }
    }

    /**
     * 保存一个配置键（后台"彻底卸载开关"等少数场景需要）
     *
     * // ⚠️ 待实例验证: 直接写 idcsmart_plugin.config(JSON) 与官方插件 setConfig() 的做法一致，
     * //   但"插件配置页保存"路径由 V10 自己走，此处仅供降级场景使用。
     *
     * @param array $values 键值对
     * @return array ['status'=>int,'msg'=>string]
     */
    public static function saveConfig(array $values)
    {
        try {
            $PluginModel = new PluginModel();
            $plugin = $PluginModel->where('name', 'IdcsmartLlmGateway')->find();
            if (empty($plugin)) {
                return ['status' => 400, 'msg' => '插件记录不存在'];
            }

            $config = json_decode((string) $plugin['config'], true);
            $config = is_array($config) ? $config : [];
            foreach ($values as $key => $value) {
                $config[$key] = $value;
            }

            $plugin->save(['config' => json_encode($config, JSON_UNESCAPED_UNICODE)]);

            self::$configCache = null;

            return ['status' => 200, 'msg' => 'ok'];
        } catch (\Throwable $e) {
            return ['status' => 400, 'msg' => $e->getMessage()];
        }
    }
}
