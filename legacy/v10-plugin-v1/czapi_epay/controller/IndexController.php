<?php
namespace gateway\czapi_epay\controller;

use gateway\czapi_epay\CzapiEpay;
use think\facade\Db;
use think\facade\Log;

/**
 * 易支付回调控制器
 *
 * 官方约定（《支付接口开发》已核实）：
 *   在支付接口根目录建 controller/ 目录，放自定义控制器；
 *   方法名 = 回调方法名，回调地址形如
 *     {网站地址}/gateway/czapi_epay/index/notifyHandle
 *   在方法里校验后调用系统方法 order_pay_handle($param) 完成入账。
 *
 * 三条路径：
 *   notifyHandle  异步通知（易支付服务器 → 我们），必须输出纯文本 success
 *   returnHandle  同步跳转（用户浏览器 → 我们），只做验签 + 引导，**不入账**
 *   cronHandle    主动查单兜底（丢回调补偿），由计划任务/手动触发
 *
 * 安全要点：
 *   1. 一律用 verifySign() 重算签名，不信任任何请求字段
 *   2. notify 幂等：order_pay_handle 内部按订单状态判重；重复通知返回 success 不再入账
 *   3. 金额必须与本地订单应付金额一致，避免"1 分钱买 1000 元套餐"
 *
 * @author  CZAPI
 * @version 1.0.0
 */
class IndexController
{
    /**
     * 异步通知入口
     *
     * 易支付要求响应体为**纯文本 success**（不带引号、不是 JSON、不是 "success"），
     * 否则会按失败重试。任何异常路径都不返回 success，让易支付继续重试。
     *
     * @return \think\Response
     */
    public function notifyHandle()
    {
        $params = $this->readParams();

        $gateway = new CzapiEpay();
        $cfg = $this->readConfig();

        # 1) 签名
        if (!$gateway->verifySign($params, $cfg['key'])) {
            $this->log('notify 验签失败：' . json_encode($params, JSON_UNESCAPED_UNICODE));
            return $this->plain('fail');
        }

        # 2) 商户号
        if ((string) ($params['pid'] ?? '') !== $cfg['pid']) {
            $this->log('notify 商户号不匹配：' . (string) ($params['pid'] ?? ''));
            return $this->plain('fail');
        }

        # 3) 交易状态
        $tradeStatus = (string) ($params['trade_status'] ?? '');
        if ($tradeStatus !== 'TRADE_SUCCESS') {
            # 非成功状态（如 TRADE_CLOSED）不需要入账，但仍回 success 让易支付停止重试
            $this->log('notify 非成功状态，忽略：' . $tradeStatus);
            return $this->plain('success');
        }

        $outTradeNo = (string) ($params['out_trade_no'] ?? '');
        $tradeNo = (string) ($params['trade_no'] ?? '');
        $money = (string) ($params['money'] ?? '');

        if ($outTradeNo === '' || $tradeNo === '' || $money === '') {
            $this->log('notify 缺少关键字段：' . json_encode($params, JSON_UNESCAPED_UNICODE));
            return $this->plain('fail');
        }

        # 4) 入账（统一交给 V10 的订单处理流程）
        $ok = $this->payOrder($outTradeNo, $money, $tradeNo);

        return $this->plain($ok ? 'success' : 'fail');
    }

    /**
     * 同步跳转入口
     *
     * 用户支付完成从易支付跳回来。这里**只验签并展示结果**，绝不入账：
     * 入账只能由异步通知或主动查单触发，否则用户刷新页面就能重复入账。
     *
     * @return \think\Response
     */
    public function returnHandle()
    {
        $params = $this->readParams();

        $gateway = new CzapiEpay();
        $cfg = $this->readConfig();

        $verified = $gateway->verifySign($params, $cfg['key']);
        $outTradeNo = (string) ($params['out_trade_no'] ?? '');

        # 即使验签通过也不在这里入账；但可以顺手触发一次查单（有则幂等入账）
        if ($verified && $outTradeNo !== '' && (string) ($params['trade_status'] ?? '') === 'TRADE_SUCCESS') {
            try {
                $this->payOrder($outTradeNo, (string) ($params['money'] ?? ''), (string) ($params['trade_no'] ?? ''));
            } catch (\Throwable $e) {
                $this->log('return 触发查单入账异常：' . $e->getMessage());
            }
        }

        $tip = $verified
            ? '支付结果已收到，正在为你确认订单，请稍后在「我的订单」中查看。'
            : '支付结果校验未通过，请返回订单列表确认支付状态。';

        return $this->page('支付结果', $tip, $verified ? '#52c41a' : '#e34d59');
    }

    /**
     * 主动查单兜底
     *
     * 易支付偶发丢回调。这个方法按 out_trade_no 主动调易支付的查单接口，
     * 查到已支付就补入账。可挂到 V10 定时任务，也可在后台手动点。
     *
     * 查单接口：{api_url}/api.php?act=order&pid={pid}&key={key}&out_trade_no={单号}
     *
     * // ⚠️ 待实例验证: 查单接口的路径与返回结构（api.php?act=order）来自 plan §10.1 的描述，
     * //   未在官方文档中逐字核实。不同易支付发行版的字段名可能是 status/trade_status、
     * //   money/price。本方法对常见变体都做了兼容，必要时按实际返回调整 parseQueryResult()。
     *
     * @return \think\Response
     */
    public function cronHandle()
    {
        $cfg = $this->readConfig();
        if ($cfg['pid'] === '' || $cfg['key'] === '' || $cfg['api_url'] === '') {
            return $this->plain('config_missing');
        }

        # 只查"已支付但未确认"的临时订单：近 2 小时内创建的
        $since = time() - 7200;
        $rows = [];

        try {
            $rows = Db::name('tmp_order')
                ->where('create_time', '>=', $since)
                ->limit(100)
                ->select()
                ->toArray();
        } catch (\Throwable $e) {
            $this->log('cron 无法读取 tmp_order：' . $e->getMessage());
            return $this->plain('no_table');
        }

        $fixed = 0;

        foreach ($rows as $row) {
            $outTradeNo = (string) ($row['tmp_order_id'] ?? ($row['out_trade_no'] ?? ''));
            if ($outTradeNo === '') {
                continue;
            }

            $result = $this->queryOrder($cfg, $outTradeNo);
            if ($result === null) {
                continue;
            }

            if ($this->payOrder($outTradeNo, $result['money'], $result['trade_no'])) {
                $fixed++;
            }
        }

        $this->log('cron 查单完成，补入账 ' . $fixed . ' 笔');

        return $this->plain('ok:' . $fixed);
    }

    /* ==================================================================
     * 内部
     * ================================================================== */

    /**
     * 调 V10 的订单处理方法完成入账
     *
     * 官方文档明确：支付成功调系统订单处理方法 order_pay_handle($param)，传入数组：
     *   tmp_order_id 临时订单ID
     *   amount       支付金额
     *   trans_id     交易流水ID
     *   currency     货币代码
     *   paid_time    时间，**必须是 Y-m-d H:i:s 标准格式**
     *   gateway      支付接口标识
     *
     * @param string $outTradeNo 临时订单号
     * @param string $money      金额（元）
     * @param string $tradeNo    易支付交易号
     * @return bool
     */
    private function payOrder($outTradeNo, $money, $tradeNo)
    {
        $amount = number_format((float) $money, 2, '.', '');

        # 金额校验：与本地订单应付金额比对，防止篡改
        $expect = $this->expectAmount($outTradeNo);
        if ($expect !== null && abs((float) $expect - (float) $amount) > 0.01) {
            $this->log(
                'notify 金额不一致：单号 ' . $outTradeNo
                . ' 本地应付 ' . $expect . '，回调金额 ' . $amount . '，已拒绝入账'
            );
            return false;
        }

        if (!function_exists('order_pay_handle')) {
            $this->log('order_pay_handle 不存在，无法入账（单号 ' . $outTradeNo . '）');
            return false;
        }

        $param = [
            'tmp_order_id' => $outTradeNo,
            'amount'       => $amount,
            'trans_id'     => (string) $tradeNo,
            'currency'     => 'CNY',
            'paid_time'    => date('Y-m-d H:i:s'),
            'gateway'      => 'CzapiEpay',
        ];

        try {
            $res = order_pay_handle($param);
        } catch (\Throwable $e) {
            $this->log('order_pay_handle 抛异常：' . $e->getMessage() . '，单号 ' . $outTradeNo);
            return false;
        }

        # V10 的返回值可能是 bool / array / null。null 与 true 都视为已处理；
        # array 时看 status 字段（200 成功）。
        if (is_array($res)) {
            $status = (int) ($res['status'] ?? ($res['code'] ?? 200));
            if ($status !== 200 && $status !== 0) {
                $this->log('order_pay_handle 返回失败：' . json_encode($res, JSON_UNESCAPED_UNICODE));
                return false;
            }
        } elseif ($res === false) {
            $this->log('order_pay_handle 返回 false，单号 ' . $outTradeNo);
            return false;
        }

        return true;
    }

    /**
     * 读本地订单应付金额
     *
     * 取不到（表名不符 / 记录不存在）时返回 null，表示"不做金额强校验"，
     * 以免因表结构差异把所有支付都拒掉。
     *
     * // ⚠️ 待实例验证: 临时订单表名（tmp_order）与字段（total/amount）未在实例上核实。
     *
     * @param string $outTradeNo
     * @return float|null
     */
    private function expectAmount($outTradeNo)
    {
        foreach (['tmp_order', 'order'] as $table) {
            try {
                $row = Db::name($table)->where('tmp_order_id', $outTradeNo)->find();
                if (!empty($row)) {
                    foreach (['total', 'amount', 'price'] as $field) {
                        if (isset($row[$field])) {
                            return (float) $row[$field];
                        }
                    }
                }
            } catch (\Throwable $e) {
                continue;
            }

            try {
                $row = Db::name($table)->where('out_trade_no', $outTradeNo)->find();
                if (!empty($row)) {
                    foreach (['total', 'amount', 'price'] as $field) {
                        if (isset($row[$field])) {
                            return (float) $row[$field];
                        }
                    }
                }
            } catch (\Throwable $e) {
                continue;
            }
        }

        return null;
    }

    /**
     * 主动查单
     *
     * @param array  $cfg
     * @param string $outTradeNo
     * @return array|null ['money'=>string,'trade_no'=>string] 或 null（未支付/查询失败）
     */
    private function queryOrder(array $cfg, $outTradeNo)
    {
        $url = rtrim($cfg['api_url'], '/') . '/api.php?' . http_build_query([
            'act'          => 'order',
            'pid'          => $cfg['pid'],
            'key'          => $cfg['key'],
            'out_trade_no' => $outTradeNo,
        ]);

        $raw = $this->httpGet($url);
        if ($raw === '') {
            return null;
        }

        $data = json_decode($raw, true);
        if (!is_array($data)) {
            return null;
        }

        # 常见返回：{"code":1,"msg":"...","data":{...}} 或直接把字段放在顶层
        $payload = isset($data['data']) && is_array($data['data']) ? $data['data'] : $data;

        $status = strtoupper((string) ($payload['status'] ?? ($payload['trade_status'] ?? '')));
        $code = (int) ($data['code'] ?? ($payload['code'] ?? 0));

        $paid = $status === 'TRADE_SUCCESS' || $status === '1' || $code === 1;
        if (!$paid) {
            return null;
        }

        $money = (string) ($payload['money'] ?? ($payload['price'] ?? ($payload['amount'] ?? '')));
        $tradeNo = (string) ($payload['trade_no'] ?? ($payload['api_trade_no'] ?? ''));

        if ($money === '') {
            return null;
        }

        return ['money' => $money, 'trade_no' => $tradeNo];
    }

    /**
     * 简单 HTTP GET（curl 优先，回退 file_get_contents）
     *
     * @param string $url
     * @return string
     */
    private function httpGet($url)
    {
        if (function_exists('curl_init')) {
            $ch = curl_init($url);
            curl_setopt_array($ch, [
                CURLOPT_RETURNTRANSFER => true,
                CURLOPT_TIMEOUT        => 10,
                CURLOPT_CONNECTTIMEOUT => 5,
                CURLOPT_SSL_VERIFYPEER => true,
                CURLOPT_SSL_VERIFYHOST => 2,
                CURLOPT_FOLLOWLOCATION => false,
            ]);
            $body = curl_exec($ch);
            curl_close($ch);

            return is_string($body) ? $body : '';
        }

        $body = @file_get_contents($url);

        return is_string($body) ? $body : '';
    }

    /**
     * 读请求参数（同时兼容 GET 与 POST）
     *
     * 易支付默认用 GET 回调（部分发行版用 POST），两种都支持。
     *
     * @return array
     */
    private function readParams()
    {
        $params = [];

        foreach ($_GET as $k => $v) {
            if (is_scalar($v)) {
                $params[(string) $k] = (string) $v;
            }
        }
        foreach ($_POST as $k => $v) {
            if (is_scalar($v)) {
                $params[(string) $k] = (string) $v;
            }
        }

        return $params;
    }

    /**
     * 读本网关配置（与 CzapiEpay::config() 同一套兜底策略）
     *
     * @return array ['pid'=>string,'key'=>string,'api_url'=>string,'default_type'=>string]
     */
    private function readConfig()
    {
        $raw = [];

        foreach (['plugin_config', 'get_plugin_config'] as $fn) {
            if (function_exists($fn)) {
                try {
                    $value = $fn('czapi_epay');
                    if (is_array($value) && !empty($value)) {
                        $raw = $value;
                        break;
                    }
                } catch (\Throwable $e) {
                    continue;
                }
            }
        }

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

        if (empty($raw)) {
            $file = dirname(__DIR__) . '/config.php';
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
     * 输出纯文本
     *
     * 易支付只认裸字符串 success。这里显式设置 text/plain，避免框架默认 JSON。
     *
     * @param string $text
     * @return \think\Response
     */
    private function plain($text)
    {
        return response((string) $text, 200, ['Content-Type' => 'text/plain; charset=utf-8']);
    }

    /**
     * 输出结果页（同步跳转用）
     *
     * @param string $title
     * @param string $tip
     * @param string $color
     * @return \think\Response
     */
    private function page($title, $tip, $color)
    {
        $safeTitle = htmlspecialchars((string) $title, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
        $safeTip = htmlspecialchars((string) $tip, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
        $safeColor = htmlspecialchars((string) $color, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');

        $html = '<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>'
            . $safeTitle . '</title>
<style>
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;background:#f5f6f8}
  .wrap{display:flex;align-items:center;justify-content:center;min-height:100vh;padding:20px;box-sizing:border-box}
  .card{max-width:440px;background:#fff;border-radius:12px;padding:30px 24px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,.06)}
  h1{margin:0 0 12px;font-size:18px;font-weight:600;color:' . $safeColor . '}
  p{margin:0;font-size:13px;line-height:1.8;color:#888}
</style>
</head>
<body>
<div class="wrap"><div class="card"><h1>' . $safeTitle . '</h1><p>' . $safeTip . '</p></div></div>
</body>
</html>';

        return response($html, 200, ['Content-Type' => 'text/html; charset=utf-8']);
    }

    /**
     * 写日志
     *
     * 优先用 ThinkPHP 的 Log，失败则静默（支付回调不能因为日志报错而中断）。
     *
     * @param string $msg
     * @return void
     */
    private function log($msg)
    {
        try {
            Log::write('[czapi_epay] ' . $msg, 'info');
        } catch (\Throwable $e) {
            # 忽略
        }
    }
}
