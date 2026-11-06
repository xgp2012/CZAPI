<?php
namespace addon\idcsmart_llm_gateway\logic;

/**
 * 免登录回调组的签名守卫
 *
 * 背景（docs/v10-notes.md §4 与 §8）：
 *   route.php 的 `console/v1/api` 组挂的是 `\app\http\middleware\Check`，
 *   它表示**不校验登录态**，绝不代表"已鉴权"。网关 -> V10 的每一次调用
 *   都必须由插件自己验 HMAC，否则任何人都能调用这些接口。
 *
 * 设计取舍：
 *   - 密钥用独立的 `callback_secret`，与 `app_secret` 隔离：
 *     app_secret 泄露时仍然无法伪造"网关 -> V10"的回调。
 *   - 拒绝时就返回 400 且不带任何业务细节，避免成为探测接口。
 *
 * @author  CZAPI
 * @version 1.0.0
 */
class CallbackGuard
{
    /**
     * 从当前请求里取签名材料并校验
     *
     * // ⚠️ 待实例验证: ThinkPHP 6 的 Request 提供 `getContent()`（原始请求体）与
     * //   `header($name)`（取请求头）。二者均为框架标准 API，但本仓库无 V10 实例，
     * //   需在目标站点确认：① 回调组能否拿到原始 body（若被 ParamFilter 改写则签名失败）；
     * //   ② 请求头是否被 Web 服务器改写大小写/下划线。
     *
     * @param \think\Request|object $request ThinkPHP 请求对象（控制器里的 $this->request）
     * @param string                $path    含前缀的路径，必须与网关签名时使用的完全一致
     * @return array ['ok'=>bool,'msg'=>string]
     */
    public static function verify($request, $path)
    {
        if (!is_object($request)) {
            return ['ok' => false, 'msg' => '无效的请求上下文'];
        }

        $body = '';
        if (method_exists($request, 'getContent')) {
            $body = (string) $request->getContent();
        }

        $method = method_exists($request, 'method') ? (string) $request->method() : 'POST';

        $timestamp = '';
        $nonce = '';
        $signature = '';
        if (method_exists($request, 'header')) {
            $timestamp = (string) $request->header('x-cz-timestamp', '');
            $nonce = (string) $request->header('x-cz-nonce', '');
            $signature = (string) $request->header('x-cz-signature', '');
            if ($signature === '') {
                # 部分部署会把头名转成下划线形式；兜底再试一次
                $signature = (string) $request->header('HTTP_X_CZ_SIGNATURE', '');
            }
        }

        # 允许把三个签名要素放在请求体里（少数反向代理会吞掉自定义头）
        if ($timestamp === '' || $nonce === '' || $signature === '') {
            $decoded = json_decode($body, true);
            if (is_array($decoded)) {
                $timestamp = $timestamp !== '' ? $timestamp : (string) ($decoded['_ts'] ?? '');
                $nonce = $nonce !== '' ? $nonce : (string) ($decoded['_nonce'] ?? '');
                $signature = $signature !== '' ? $signature : (string) ($decoded['_sign'] ?? '');
            }
        }

        return LlmGatewayLogic::verifyCallback($method, $path, $body, $timestamp, $nonce, $signature);
    }
}
