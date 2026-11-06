<?php
namespace addon\idcsmart_llm_gateway\controller;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use addon\idcsmart_llm_gateway\model\LlmApi;
use app\event\controller\PluginAdminBaseController;

/**
 * 后台渠道状态与模型目录
 *
 * 边界（plan §8.1 / 附录 C）：**渠道的增删改在网关的 Vue3 后台做**，
 * V10 插件后台只做"看状态 + 看可售模型"，避免两处配置互相覆盖。
 *
 * // ⚠️ 待实例验证: 网关 /admin/v1/channels 需要管理员 JWT，插件无法代持，
 * //   因此这里只用免密（HMAC）的 /internal/v10/health 与 /internal/v10/models 取数。
 * //   若后续希望在 V10 后台直接改渠道，需网关新增 internal 侧渠道只读/写入接口。
 *
 * @title LLM网关渠道(后台)
 * @desc LLM网关渠道(后台)
 * @use addon\idcsmart_llm_gateway\controller\ChannelController
 */
class ChannelController extends PluginAdminBaseController
{
    /**
     * 时间 2026-11-05
     * @title 网关连通状态
     * @desc 调 /internal/v10/health，返回状态灯所需的字段
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/channels/status
     * @method GET
     * @return int ok - desc:1连通 0不通
     * @return string version - desc:网关版本
     * @return object flags - desc:功能开关
     */
    public function status()
    {
        $res = LlmApi::health();

        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 200,
                'msg'    => lang_plugins('success_message'),
                'data'   => [
                    'ok'      => 0,
                    'error'   => $res['msg'],
                    'latency' => 0,
                ],
            ]);
        }

        $data = is_array($res['data']) ? $res['data'] : [];

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'ok'      => 1,
                'version' => (string) ($data['version'] ?? ''),
                'time'    => (int) ($data['time'] ?? 0),
                'db'      => (string) ($data['db'] ?? ''),
                'cache'   => (string) ($data['cache'] ?? ''),
                'queue'   => (string) ($data['queue'] ?? ''),
                'flags'   => $data['flags'] ?? [],
            ],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 可售模型目录
     * @desc 调 /internal/v10/models；用于核对"网关实际在卖什么"
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/models
     * @method GET
     * @param string keyword - desc:按模型名过滤 validate:optional
     * @return array list - desc:模型列表
     * @return int count - desc:总数
     */
    public function modelList()
    {
        $res = LlmApi::models();
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
            ]);
        }

        $items = $res['data']['items'] ?? [];
        $keyword = trim((string) $this->request->param('keyword', ''));
        if ($keyword !== '') {
            $items = array_values(array_filter($items, function ($item) use ($keyword) {
                return stripos((string) ($item['model_name'] ?? ''), $keyword) !== false;
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
     * @title 同步模型目录
     * @desc 拉一次网关模型目录并回传条数（本地不落库，避免与网关双份事实来源）
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/models/sync
     * @method POST
     */
    public function syncModels()
    {
        $res = LlmApi::models();
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
            ]);
        }

        $items = $res['data']['items'] ?? [];
        $enabled = 0;
        foreach ($items as $item) {
            if ((int) ($item['status'] ?? 1) === 1) {
                $enabled++;
            }
        }

        LlmGatewayLogic::audit(
            'LLM网关：同步模型目录，共' . count($items) . '个（启用' . $enabled . '）',
            (int) get_admin_id()
        );

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['count' => count($items), 'enabled' => $enabled],
        ]);
    }
}
