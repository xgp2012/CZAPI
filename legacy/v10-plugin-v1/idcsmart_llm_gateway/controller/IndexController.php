<?php
namespace addon\idcsmart_llm_gateway\controller;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use addon\idcsmart_llm_gateway\model\KeyModel;
use addon\idcsmart_llm_gateway\model\LlmApi;
use addon\idcsmart_llm_gateway\model\PushLogModel;
use addon\idcsmart_llm_gateway\model\TxnModel;
use app\event\controller\PluginAdminBaseController;

/**
 * 后台仪表盘
 *
 * 基类必须是 app\event\controller\PluginAdminBaseController —— 源码实证（docs/v10-notes.md §3）。
 *
 * @title LLM网关(后台)
 * @desc LLM网关(后台)
 * @use addon\idcsmart_llm_gateway\controller\IndexController
 */
class IndexController extends PluginAdminBaseController
{
    /**
     * 时间 2026-11-05
     * @title 仪表盘
     * @desc 网关连通性 + 本地数据概况 + 推送队列积压
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/dashboard
     * @method GET
     * @return int key_total - desc:Key 总数
     * @return int key_active - desc:可用 Key 数
     * @return int pushlog_failed - desc:待重试推送数
     * @return object health - desc:网关健康检查结果
     */
    public function dashboard()
    {
        # 网关健康检查：失败不视为接口失败，前端据此显示红灯
        $health = LlmApi::health();
        $healthOk = (int) $health['code'] === LlmApi::CODE_OK;

        $keyTotal = (int) KeyModel::where('id', '>', 0)->count();
        $keyActive = (int) KeyModel::where('status', 'active')->count();
        $keySuspended = (int) KeyModel::where('status', 'suspended')->count();
        $keyRevoked = (int) KeyModel::where('status', 'revoked')->count();

        $pushFailed = (int) PushLogModel::where('status', 'failed')->count();
        $pushPending = (int) PushLogModel::where('status', 'pending')->count();

        $redeemTotal = (int) TxnModel::where('kind', 'redeem_code')->count();

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'health_ok'    => $healthOk,
                'health'       => $healthOk ? ($health['data'] ?? []) : ['error' => $health['msg']],
                'key_total'    => $keyTotal,
                'key_active'   => $keyActive,
                'key_suspended' => $keySuspended,
                'key_revoked'  => $keyRevoked,
                'push_failed'  => $pushFailed,
                'push_pending' => $pushPending,
                'redeem_total' => $redeemTotal,
                'gateway_base_url' => (string) LlmGatewayLogic::config('gateway_base_url', ''),
                'app_id'       => (string) LlmGatewayLogic::config('app_id', ''),
                # secret 只回显尾 4 位（与官方插件一致的做法）
                'app_secret_tail' => $this->secretTail(),
            ],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 读取插件配置（只读，脱敏）
     * @desc 真正的配置写入走 V10 自带的「插件配置」页（config.php 自动渲染）
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/config
     * @method GET
     * @return string gateway_base_url - desc:网关地址
     * @return string app_id - desc:App ID
     * @return string app_secret_tail - desc:App Secret 尾4位
     * @return array plan_map - desc:套餐映射
     */
    public function getConfig()
    {
        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'gateway_base_url'     => (string) LlmGatewayLogic::config('gateway_base_url', ''),
                'app_id'               => (string) LlmGatewayLogic::config('app_id', ''),
                'app_secret_tail'      => $this->secretTail(),
                'plan_map'             => LlmGatewayLogic::planMap(),
                'default_billing_mode' => (string) LlmGatewayLogic::config('default_billing_mode', 'hybrid'),
                'max_keys_per_host'    => LlmGatewayLogic::configInt('max_keys_per_host', 5),
                'timeout_ms'           => LlmGatewayLogic::configInt('timeout_ms', 5000),
                'keep_data_on_uninstall' => (string) LlmGatewayLogic::config('keep_data_on_uninstall', '1'),
            ],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 设置卸载策略
     * @desc 只在后台"彻底卸载"开关上使用；其余配置请走 V10 插件配置页
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/config
     * @method POST
     * @param int keep_data_on_uninstall - desc:1 保留数据 0 彻底卸载 validate:required
     */
    public function setConfig()
    {
        $param = $this->request->param();
        $keep = (string) ($param['keep_data_on_uninstall'] ?? '');

        if ($keep !== '0' && $keep !== '1') {
            return json(['status' => 400, 'msg' => lang_plugins('param_error')]);
        }

        $res = LlmGatewayLogic::saveConfig(['keep_data_on_uninstall' => $keep]);
        if ((int) $res['status'] !== 200) {
            return json(['status' => 400, 'msg' => $res['msg']]);
        }

        LlmGatewayLogic::audit(
            'LLM网关：设置卸载策略 keep_data_on_uninstall=' . $keep,
            (int) get_admin_id()
        );

        return json(['status' => 200, 'msg' => lang_plugins('success_message')]);
    }

    /**
     * App Secret 尾 4 位
     *
     * @return string
     */
    private function secretTail()
    {
        $secret = (string) LlmGatewayLogic::config('app_secret', '');
        if ($secret === '') {
            return '';
        }

        return strlen($secret) <= 4 ? '****' : '****' . substr($secret, -4);
    }
}
