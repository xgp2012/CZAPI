<?php
namespace gateway\czapi_epay;

/**
 * CZAPI 易支付网关插件（V10 支付接口）
 *
 * 目录：public/plugins/gateway/czapi_epay/
 * 命名空间 = gateway\{目录名}；类名 = 目录名大驼峰（官方《支付接口开发》文档「位置 / 目录结构」）。
 *
 * ── 与官方文档的逐条对应 ─────────────────────────────────────────────
 *   1. 必须有 $info（name/title/description/author/version + 可选 help_url/author_url/url）
 *   2. 定义 $orderRule（临时订单生成规则）
 *   3. 必须实现 install() / uninstall()
 *   4. 必须实现入口方法：目录名大驼峰 + Handle，即 CzapiEpayHandle($param)，**返回 HTML**
 *      系统把这段 HTML 渲染到支付输出页（不是 redirect，也不是 JSON）
 *   5. 可选 config.php 定义后台配置表单
 *   6. 异步/同步回调放 controller/ 目录，URL 形如
 *      {站点}/gateway/czapi_epay/index/notifyHandle
 *      回调里校验签名后调系统方法 order_pay_handle($param) 入账
 *
 * ── 为什么"收款只在这里"（plan §10.1）────────────────────────────────
 * 网关（Go）侧不实现任何支付逻辑。充值、套餐、卡密全部走 V10 订单体系，
 * 因此开票/退款/对账口径天然统一，两端不需要对资金流。
 *
 * @author  CZAPI
 * @version 1.0.0
 */
class CzapiEpay
{
    /**
     * 插件基础信息（后台「支付接口」列表显示）
     *
     * name 必须与目录名大驼峰一致且全局唯一——它同时是 order_pay_handle() 里
     * 传给 V10 的 gateway 标识。
     *
     * @var array
     */
    public $info = [
        'name'        => 'CzapiEpay',
        'title'       => '易支付',
        'description' => '通过易支付（彩虹易支付 / 兼容接口）收款，支持支付宝、微信、QQ 钱包',
        'author'      => 'CZAPI',
        'version'     => '1.0.0',
        'help_url'    => '',
        'author_url'  => '',
        'url'         => '',
    ];

    /**
     * 临时订单生成规则
     *
     * 1 = 毫秒时间戳 + 8 位随机数（21-22 位），易支付 out_trade_no 通常限 32 位以内，合适。
     *
     * @var int
     */
    public $orderRule = 1;

    /**
     * 安装
     *
     * 本插件不建表：所有订单与流水都落在 V10 既有表里（plan §10.1 的设计前提）。
     * 保留方法是为了满足官方「必须实现 install()」的约定。
     *
     * @return bool
     */
    public function install()
    {
        return true;
    }

    /**
     * 卸载
     *
     * 同样不删任何数据：V10 会自行清理 idcsmart_plugin 中本网关的记录，
     * 已发生的支付流水必须保留以备对账。
     *
     * @return bool
     */
    public function uninstall()
    {
        return true;
    }

    /**
     * 发起支付（入口方法）
     *
     * 方法名规则：目录名大驼峰 + Handle（官方文档明确写死这个方法签名）。
     * 返回值是 **HTML 字符串**，系统渲染到支付输出页。
     *
     * $param 结构（官方文档已核实）：
     *   out_trade_no  临时订单号
     *   client        {id, username, company, address, country, phone_code, phone, email}
     *   product       ['魔方云', ...] 商品名数组
     *   global        {website_name, website_url}
     *   finance       {id, total, tmp_order_id, tmp_order_id2, tmp_order_id3}
     *
     * @param array $param
     * @return string HTML
     */
    public function CzapiEpayHandle($param)
    {
        $cfg = $this->config();

        if ($cfg['pid'] === '' || $cfg['key'] === '' || $cfg['api_url'] === '') {
            return $this->renderError('易支付未配置完整：请在后台「支付接口 → 易支付」里填写 pid、key、api_url');
        }

        $tempOrderNo = (string) ($param['out_trade_no'] ?? '');
        $finance = $param['finance'] ?? [];
        $realOrderId = (string) ($finance['id'] ?? '');

        if ($tempOrderNo === '') {
            return $this->renderError('缺少临时订单号（out_trade_no），无法发起支付');
        }

        # 金额：V10 传的是元，保留两位小数。易支付要求 money 为不带货币符号的数值
        $money = number_format((float) ($finance['total'] ?? 0), 2, '.', '');
        if ((float) $money <= 0) {
            return $this->renderError('订单金额非法，无法发起支付');
        }

        $siteName = (string) ($param['global']['website_name'] ?? '');
        if ($siteName === '') {
            $siteName = 'IDC';
        }

        $siteUrl = rtrim((string) ($param['global']['website_url'] ?? ''), '/');
        if ($siteUrl === '') {
            # 兜底：站内相对地址无法用于 notify_url，必须绝对地址
            return $this->renderError('站点地址（global.website_url）为空，无法生成支付回调地址');
        }

        # 商品名：取商品数组拼接，最多保留到易支付可接受的长度
        $productNames = $param['product'] ?? [];
        $name = is_array($productNames) ? implode('、', array_filter(array_map('strval', $productNames))) : '';
        if ($name === '') {
            $name = '订单 ' . $realOrderId;
        }
        $name = mb_substr($name, 0, 100, 'UTF-8');

        # 支付方式：本网关把三种渠道拆成三个 type，前端 selected 由 V10 传入（若有）
        $type = $this->resolveType($param);

        # 回调地址：官方约定的支付接口控制器路径
        $notifyUrl = $siteUrl . '/gateway/czapi_epay/index/notifyHandle';
        $returnUrl = $siteUrl . '/gateway/czapi_epay/index/returnHandle';

        # 组装易支付参数（plan §10.1「必填参数」）
        $params = [
            'pid'          => $cfg['pid'],
            'type'         => $type,
            'out_trade_no' => $tempOrderNo,
            'notify_url'   => $notifyUrl,
            'return_url'   => $returnUrl,
            'name'         => $name,
            'money'        => $money,
            'sitename'     => $siteName,
        ];

        $params['sign'] = $this->sign($params, $cfg['key']);
        $params['sign_type'] = 'MD5';

        # 提交方式：跳转收银台（表单自动提交）——兼容性最好，不依赖前端弹窗能力
        $action = rtrim($cfg['api_url'], '/') . '/submit.php';

        return $this->renderAutoSubmitForm($action, $params, $money, $type, $siteName);
    }

    /* ==================================================================
     * 内部：配置与签名
     * ================================================================== */

    /**
     * 读本网关的配置
     *
     * V10 把 config.php 定义的字段存在插件配置里，通过 config('config') 或
     * 本类的 config 属性可取到。这里做了多重兜底，任一取到即可。
     *
     * // ⚠️ 待实例验证: 支付网关插件内读取自身配置的官方途径未在文档中写明
     * //   （文档只说明 config.php 会在"插件设置"里渲染成表单）。本方法依次尝试
     * //   1) 全局函数 plugin_config()  2) config('config')  3) 常量/环境变量
     * //   上线前请在实例上确认后台保存的配置到底从哪儿读，必要时只保留正确的那一条。
     *
     * @return array ['pid'=>string,'key'=>string,'api_url'=>string,'default_type'=>string]
     */
    private function config()
    {
        $raw = [];

        # 1) V10 常见的插件配置读取函数
        foreach (['plugin_config', 'get_plugin_config'] as $fn) {
            if (function_exists($fn)) {
                try {
                    $value = $fn('czapi_epay');
                    if (is_array($value) && !empty($value)) {
                        $raw = $value;
                        break;
                    }
                } catch (\Throwable $e) {
                    # 继续尝试下一种
                }
            }
        }

        # 2) 框架配置
        if (empty($raw) && function_exists('config')) {
            try {
                $value = config('config');
                if (is_array($value) && !empty($value)) {
                    $raw = $value;
                }
            } catch (\Throwable $e) {
                # 忽略
            }
        }

        # 3) 同目录 config.php 的默认值兜底（只取 value，不覆盖已存配置）
        if (empty($raw)) {
            $file = __DIR__ . '/config.php';
            if (is_file($file)) {
                try {
                    $defaults = include $file;
                    if (is_array($defaults)) {
                        foreach ($defaults as $k => $item) {
                            if (is_array($item) && array_key_exists('value', $item)) {
                                $raw[$k] = $item['value'];
                            }
                        }
                    }
                } catch (\Throwable $e) {
                    # 忽略
                }
            }
        }

        return [
            'pid'          => trim((string) ($raw['pid'] ?? '')),
            'key'          => trim((string) ($raw['key'] ?? '')),
            'api_url'      => trim((string) ($raw['api_url'] ?? '')),
            'default_type' => trim((string) ($raw['default_type'] ?? 'alipay')),
        ];
    }

    /**
     * 易支付签名（plan §10.1）
     *
     * 规则：剔除空值、sign、sign_type → 键名 ASCII 升序 → 拼 k=v&k=v → 末尾直接拼 key → MD5 小写。
     * 注意末尾是"直接拼 key"，没有 &key= 前缀，这是易支付/彩虹易支付的约定。
     *
     * @param array  $params
     * @param string $key
     * @return string 32 位小写 md5
     */
    public function sign(array $params, $key)
    {
        $filtered = [];
        foreach ($params as $k => $v) {
            if ($k === 'sign' || $k === 'sign_type') {
                continue;
            }
            if ($v === '' || $v === null) {
                continue;
            }
            $filtered[$k] = (string) $v;
        }

        ksort($filtered, SORT_STRING);

        $pairs = [];
        foreach ($filtered as $k => $v) {
            $pairs[] = $k . '=' . $v;
        }

        return md5(implode('&', $pairs) . $key);
    }

    /**
     * 校验易支付回调签名
     *
     * 回调参数：pid, trade_no, out_trade_no, type, money, trade_status, sign, sign_type
     * 用同样的规则重算并 hash_equals 比对（防时序攻击）。
     *
     * @param array  $params 回调原始参数（$_GET 或 $_POST）
     * @param string $key
     * @return bool
     */
    public function verifySign(array $params, $key)
    {
        $sign = (string) ($params['sign'] ?? '');
        if ($sign === '' || $key === '') {
            return false;
        }

        $expected = $this->sign($params, $key);

        return hash_equals($expected, strtolower($sign));
    }

    /**
     * 解析支付方式
     *
     * V10 传的支付方式可能出现在 gateway / pay_type / type 里，值可能是中文名或英文标识。
     * 统一映射到易支付支持的 alipay / wxpay / qqpay。
     *
     * @param array $param
     * @return string
     */
    private function resolveType($param)
    {
        $candidates = [
            (string) ($param['pay_type'] ?? ''),
            (string) ($param['type'] ?? ''),
            (string) ($param['gateway_type'] ?? ''),
        ];

        foreach ($candidates as $raw) {
            $raw = strtolower(trim($raw));
            if ($raw === '') {
                continue;
            }
            if (in_array($raw, ['alipay', 'wxpay', 'qqpay'], true)) {
                return $raw;
            }
            if (strpos($raw, 'wx') !== false || strpos($raw, 'wechat') !== false || strpos($raw, 'weixin') !== false) {
                return 'wxpay';
            }
            if (strpos($raw, 'qq') !== false) {
                return 'qqpay';
            }
            if (strpos($raw, 'ali') !== false) {
                return 'alipay';
            }
        }

        $default = strtolower($this->config()['default_type']);
        if (in_array($default, ['alipay', 'wxpay', 'qqpay'], true)) {
            return $default;
        }

        return 'alipay';
    }

    /* ==================================================================
     * 内部：HTML 输出
     * ================================================================== */

    /**
     * 自动提交表单（跳转到易支付收银台）
     *
     * 官方要求入口方法返回 HTML，系统渲染到支付输出页。用自动提交表单而不是 302，
     * 是因为易支付的 submit.php 只接受 POST 参数（GET 会丢签名）。
     *
     * @param string $action
     * @param array  $params
     * @param string $money
     * @param string $type
     * @param string $siteName
     * @return string
     */
    private function renderAutoSubmitForm($action, array $params, $money, $type, $siteName)
    {
        $inputs = '';
        foreach ($params as $k => $v) {
            $inputs .= '<input type="hidden" name="' . $this->esc($k) . '" value="' . $this->esc($v) . '">' . "\n";
        }

        $typeText = ['alipay' => '支付宝', 'wxpay' => '微信支付', 'qqpay' => 'QQ 钱包'][$type] ?? $type;

        return '<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>正在跳转支付…</title>
<style>
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;background:#f5f6f8;color:#333}
  .wrap{display:flex;align-items:center;justify-content:center;min-height:100vh;padding:20px;box-sizing:border-box}
  .card{width:100%;max-width:420px;background:#fff;border-radius:12px;padding:32px 24px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,.06)}
  .spinner{width:36px;height:36px;margin:0 auto 18px;border:3px solid rgba(64,158,255,.2);border-top-color:#409eff;border-radius:50%;animation:spin .9s linear infinite}
  @keyframes spin{to{transform:rotate(360deg)}}
  h1{margin:0 0 8px;font-size:18px;font-weight:600}
  p{margin:0;font-size:13px;line-height:1.7;color:#888}
  .money{margin:16px 0;font-size:26px;font-weight:700;color:#e34d59}
  .btn{display:inline-block;margin-top:18px;padding:10px 26px;border:0;border-radius:8px;background:#409eff;color:#fff;font-size:14px;cursor:pointer}
  .btn:hover{opacity:.9}
</style>
</head>
<body>
<div class="wrap">
  <div class="card">
    <div class="spinner"></div>
    <h1>正在跳转到' . $this->esc($typeText) . '…</h1>
    <div class="money">￥' . $this->esc($money) . '</div>
    <p>' . $this->esc($siteName) . '<br>如未自动跳转，请点击下方按钮</p>
    <form id="epay-form" action="' . $this->esc($action) . '" method="post">
' . $inputs . '      <button type="submit" class="btn">立即支付</button>
    </form>
  </div>
</div>
<script>document.getElementById("epay-form").submit();</script>
</body>
</html>';
    }

    /**
     * 错误页 HTML（不抛异常：V10 会把这里的内容直接渲染给用户）
     *
     * @param string $msg
     * @return string
     */
    private function renderError($msg)
    {
        return '<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>支付暂不可用</title>
<style>
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;background:#f5f6f8}
  .wrap{display:flex;align-items:center;justify-content:center;min-height:100vh;padding:20px;box-sizing:border-box}
  .card{max-width:440px;background:#fff;border-radius:12px;padding:28px 24px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,.06)}
  .icon{font-size:36px;line-height:1;margin-bottom:12px}
  h1{margin:0 0 10px;font-size:17px;font-weight:600;color:#333}
  p{margin:0;font-size:13px;line-height:1.7;color:#888;word-break:break-all}
</style>
</head>
<body>
<div class="wrap">
  <div class="card">
    <div class="icon">⚠️</div>
    <h1>支付暂不可用</h1>
    <p>' . $this->esc($msg) . '</p>
  </div>
</div>
</body>
</html>';
    }

    /**
     * HTML 转义
     *
     * @param mixed $value
     * @return string
     */
    private function esc($value)
    {
        return htmlspecialchars((string) $value, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
    }
}
