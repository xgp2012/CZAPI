<?php
namespace addon\idcsmart_llm_gateway\controller;

use addon\idcsmart_llm_gateway\model\LlmApi;
use addon\idcsmart_llm_gateway\model\PushLogModel;
use app\event\controller\PluginAdminBaseController;

/**
 * 后台推送队列（可靠投递可视化 + 手动重投）
 *
 * 这是"客户投诉充了没到账"时的自助修复入口（plan §10.3 最后一条）。
 *
 * @title LLM网关推送队列(后台)
 * @desc LLM网关推送队列(后台)
 * @use addon\idcsmart_llm_gateway\controller\PushLogController
 */
class PushLogController extends PluginAdminBaseController
{
    /**
     * 时间 2026-11-05
     * @title 队列列表
     * @desc 按状态 / 单据号检索推送记录
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/pushlog
     * @method GET
     * @param string status - desc:pending|done|failed validate:optional
     * @param string ref_id - desc:业务单号模糊搜索 validate:optional
     * @param int page - desc:页码 validate:optional
     * @param int limit - desc:每页条数 validate:optional
     * @return array list - desc:队列内容
     * @return int count - desc:总数
     */
    public function pushLogList()
    {
        return json((new PushLogModel())->queueList($this->request->param()));
    }

    /**
     * 时间 2026-11-05
     * @title 手动重投单条
     * @desc 立即重投一条失败的推送（幂等由网关 (ref_type,ref_id) 保证，重投不会重复加额）
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/pushlog/:id/retry
     * @method POST
     * @param int id - desc:推送记录ID validate:required
     */
    public function retry()
    {
        $id = (int) $this->request->param('id', 0);
        if ($id <= 0) {
            return json(['status' => 400, 'msg' => lang_plugins('param_error')]);
        }

        $res = LlmApi::retryOne($id);

        active_log('LLM网关：手动重投推送#' . $id . ' 管理员#admin' . get_admin_id(), 'addon_idcsmart_llm_gateway', $id);

        $ok = (int) $res['code'] === LlmApi::CODE_OK;

        return json([
            'status' => $ok ? 200 : 400,
            'msg'    => $res['msg'] !== '' ? $res['msg'] : ($ok ? lang_plugins('success_message') : lang_plugins('error_message')),
            'data'   => ['code' => (int) $res['code']],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 批量重投（扫描到期的失败记录）
     * @desc 等价于定时任务的 retryFailed()，供运维手动触发
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/pushlog/retry-all
     * @method POST
     * @param int limit - desc:本次最多处理多少条，默认50 validate:optional
     */
    public function retryAll()
    {
        $limit = (int) $this->request->param('limit', 50);
        if ($limit <= 0 || $limit > 500) {
            $limit = 50;
        }

        $stat = LlmApi::retryFailed($limit);

        active_log(
            'LLM网关：批量重投推送 扫描' . $stat['scanned'] . ' 成功' . $stat['done'] . ' 失败' . $stat['failed']
            . ' 管理员#admin' . get_admin_id(),
            'addon_idcsmart_llm_gateway',
            (int) get_admin_id()
        );

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => $stat,
        ]);
    }
}
