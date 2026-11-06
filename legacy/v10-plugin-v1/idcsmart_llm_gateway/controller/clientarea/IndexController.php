<?php
namespace addon\idcsmart_llm_gateway\controller\clientarea;

use addon\idcsmart_llm_gateway\model\LlmApi;
use addon\idcsmart_llm_gateway\model\UsageModel;
use app\event\controller\PluginBaseController;

/**
 * 前台（会员中心）概览 + SSO 票据
 *
 * 基类必须是 app\event\controller\PluginBaseController —— 源码实证（docs/v10-notes.md §3），
 * 官方文档写的 app\admin\controller\PluginBaseController 是错的。
 *
 * @title LLM 网关(会员中心)
 * @desc LLM 网关(会员中心)
 * @use addon\idcsmart_llm_gateway\controller\clientarea\IndexController
 */
class IndexController extends PluginBaseController
{
    /**
     * 时间 2026-11-05
     * @title 概览（额度 + 用量）
     * @desc 余额、套餐额度、今日/本月用量、近 30 天趋势
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/overview
     * @method GET
     * @param int host_id - desc:可选，只看某个产品 desc
     * @return int balance_micros - desc:余额，微元
     * @return array subscriptions - desc:套餐列表
     * @return array today - desc:今日用量
     * @return array month - desc:本月用量
     * @return array trend - desc:近30天趋势
     */
    public function overview()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $res = LlmApi::quota((int) $clientId);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
                'data'   => null,
            ]);
        }

        $quota = is_array($res['data']) ? $res['data'] : [];
        $now = time();

        # 用量：优先取网关实时数据，网关不可达时退回本地快照
        $todaySummary = LlmApi::usageSummary([
            'v10_client_id' => (int) $clientId,
            'from'          => strtotime('today'),
            'to'            => $now,
            'group_by'      => 'day',
        ]);
        $monthSummary = LlmApi::usageSummary([
            'v10_client_id' => (int) $clientId,
            'from'          => strtotime(date('Y-m-01 00:00:00')),
            'to'            => $now,
            'group_by'      => 'day',
        ]);

        $today = (int) $todaySummary['code'] === LlmApi::CODE_OK && isset($todaySummary['data']['totals'])
            ? $todaySummary['data']['totals']
            : UsageModel::totalByClient((int) $clientId, strtotime('today'), $now);

        $month = (int) $monthSummary['code'] === LlmApi::CODE_OK && isset($monthSummary['data']['totals'])
            ? $monthSummary['data']['totals']
            : UsageModel::totalByClient((int) $clientId, strtotime(date('Y-m-01 00:00:00')), $now);

        $trend = (int) $monthSummary['code'] === LlmApi::CODE_OK && !empty($monthSummary['data']['buckets'])
            ? $monthSummary['data']['buckets']
            : UsageModel::dailyByClient((int) $clientId, $now - 86400 * 30, $now);

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'account'        => [
                    'v10_client_id' => (int) $clientId,
                    'email'         => (string) ($this->request->client_name ?? ''),
                    'status'        => (int) ($quota['status'] ?? 1),
                ],
                'balance_micros' => (int) ($quota['balance_micros'] ?? 0),
                'subscriptions'  => $quota['subscriptions'] ?? [],
                'today'          => $today,
                'month'          => $month,
                'trend'          => $trend,
            ],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 控制台 SSO 票据
     * @desc 为已登录客户申请一次性入驻票据，前端 iframe 壳页面用它打开网关 Vue3 控制台
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/sso-ticket
     * @method GET
     * @return string ticket - desc:一次性票据（60 秒有效）
     * @return int expires_in - desc:有效期秒数
     * @return string sso_url - desc:控制台跳转地址，直接赋给 iframe.src
     */
    public function ssoTicket()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $email = (string) ($this->request->client_name ?? '');
        if ($email === '') {
            # client_name 可能是用户名而非邮箱；票据接口只要求"能标识客户"，空串也可接受，
            # 但为便于网关侧排查，这里尽量补一个稳定值。
            $email = 'client' . (int) $clientId . '@v10.local';
        }

        $res = LlmApi::ssoTicket((int) $clientId, $email);
        if ((int) $res['code'] !== LlmApi::CODE_OK || empty($res['data']['sso_url'])) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_sso_failed'),
                'data'   => null,
            ]);
        }

        $data = is_array($res['data']) ? $res['data'] : [];

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'ticket'     => (string) ($data['ticket'] ?? ''),
                'expires_in' => (int) ($data['expires_in'] ?? 60),
                'sso_url'    => (string) $data['sso_url'],
            ],
        ]);
    }
}
