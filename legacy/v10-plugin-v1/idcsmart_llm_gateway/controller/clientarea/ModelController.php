<?php
namespace addon\idcsmart_llm_gateway\controller\clientarea;

use addon\idcsmart_llm_gateway\model\LlmApi;
use app\event\controller\PluginBaseController;

/**
 * 前台（会员中心）模型列表
 *
 * 契约：docs/internal-api-v1.md §2.9 GET /internal/v1/models
 * 网关返回**可售模型目录**；本控制器只负责按客户可用范围透出，不自行改价或改倍率。
 *
 * @title LLM网关模型(会员中心)
 * @desc LLM网关模型(会员中心)
 * @use addon\idcsmart_llm_gateway\controller\clientarea\ModelController
 */
class ModelController extends PluginBaseController
{
    /**
     * 时间 2026-11-05
     * @title 模型列表
     * @desc 可售模型目录（含上下文长度、能力标签、倍率与价格提示）
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/models
     * @method GET
     * @param string keyword - desc:按模型名/显示名过滤 validate:optional
     * @return array list - desc:模型列表
     * @return int count - desc:总数
     */
    public function modelList()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $res = LlmApi::models();
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
                'data'   => null,
            ]);
        }

        $items = $res['data']['items'] ?? [];

        # 只展示启用中的模型
        $items = array_values(array_filter($items, function ($item) {
            return (int) ($item['status'] ?? 1) === 1;
        }));

        $keyword = trim((string) $this->request->param('keyword', ''));
        if ($keyword !== '') {
            $items = array_values(array_filter($items, function ($item) use ($keyword) {
                $name = (string) ($item['model_name'] ?? '');
                $display = (string) ($item['display_name'] ?? '');

                return stripos($name, $keyword) !== false || stripos($display, $keyword) !== false;
            }));
        }

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['list' => $items, 'count' => count($items)],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 用量汇总
     * @desc 按 day|model|key|host 分组
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/usage/summary
     * @method GET
     * @param int from - desc:开始时间戳 validate:optional
     * @param int to - desc:结束时间戳 validate:optional
     * @param string group_by - desc:day|model|key|host validate:optional
     * @param int api_key_id - desc:按Key过滤 validate:optional
     * @return array totals - desc:合计
     * @return array buckets - desc:分组明细
     */
    public function usageSummary()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $param = $this->request->param();
        $groupBy = (string) ($param['group_by'] ?? 'day');
        if (!in_array($groupBy, ['day', 'model', 'key', 'host'], true)) {
            $groupBy = 'day';
        }

        $res = LlmApi::usageSummary([
            'v10_client_id' => (int) $clientId,
            'from'          => (int) ($param['from'] ?? (time() - 86400 * 30)),
            'to'            => (int) ($param['to'] ?? time()),
            'group_by'      => $groupBy,
            'api_key_id'    => $param['api_key_id'] ?? null,
        ]);

        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
                'data'   => null,
            ]);
        }

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'totals'  => $res['data']['totals'] ?? [],
                'buckets' => $res['data']['buckets'] ?? [],
            ],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 用量明细
     * @desc 明细分页，**不含 prompt/response 正文**（契约 §4 合规要求）
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/usage/logs
     * @method GET
     * @param int from - desc:开始时间戳 validate:optional
     * @param int to - desc:结束时间戳 validate:optional
     * @param int api_key_id - desc:按Key过滤 validate:optional
     * @param string model - desc:按模型过滤 validate:optional
     * @param int page - desc:页码，从1开始 validate:optional
     * @param int page_size - desc:每页条数，<=200 validate:optional
     * @return array list - desc:明细
     * @return int count - desc:总数
     */
    public function usageLogs()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $param = $this->request->param();
        $res = LlmApi::usageLogs([
            'v10_client_id' => (int) $clientId,
            'api_key_id'    => $param['api_key_id'] ?? null,
            'model'         => $param['model'] ?? null,
            'from'          => (int) ($param['from'] ?? (time() - 86400 * 30)),
            'to'            => (int) ($param['to'] ?? time()),
            'page'          => max(1, (int) ($param['page'] ?? 1)),
            'page_size'     => (int) ($param['page_size'] ?? 20),
        ]);

        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
                'data'   => null,
            ]);
        }

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'list'      => $res['data']['items'] ?? [],
                'count'     => (int) ($res['data']['total'] ?? 0),
                'page'      => (int) ($res['data']['page'] ?? 1),
                'page_size' => (int) ($res['data']['page_size'] ?? 20),
            ],
        ]);
    }
}
