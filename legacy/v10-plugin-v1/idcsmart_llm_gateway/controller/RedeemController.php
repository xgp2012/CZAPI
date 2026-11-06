<?php
namespace addon\idcsmart_llm_gateway\controller;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use addon\idcsmart_llm_gateway\model\RedeemModel;
use addon\idcsmart_llm_gateway\model\TxnModel;
use addon\idcsmart_llm_gateway\validate\RedeemValidate;
use app\event\controller\PluginAdminBaseController;

/**
 * 后台卡密批次生成 / 导出 / 禁用
 *
 * plan §10.2：后台生成批次 -> 导出 CSV 给渠道 -> 前台兑换。
 * **明文只在生成这一次返回**，之后库里只有 sha256 与前缀。
 *
 * @title LLM网关卡密(后台)
 * @desc LLM网关卡密(后台)
 * @use addon\idcsmart_llm_gateway\controller\RedeemController
 */
class RedeemController extends PluginAdminBaseController
{
    private $validate = null;

    public function initialize()
    {
        parent::initialize();
        $this->validate = new RedeemValidate();
    }

    /**
     * 时间 2026-11-05
     * @title 生成卡密批次
     * @desc 生成 N 张卡密；返回的 codes 是唯一一次明文，请立即导出
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/redeem/batch
     * @method POST
     * @param int count - desc:数量，1-5000 validate:required
     * @param float amount_yuan - desc:每张面额（元） validate:required
     * @param int expires_at - desc:过期时间戳，0不过期 validate:optional
     * @param string batch_no - desc:自定义批次号 validate:optional
     * @return string batch_no - desc:批次号
     * @return array codes - desc:卡密明文（仅此一次）
     */
    public function createBatch()
    {
        $param = $this->request->param();

        if (!$this->validate->scene('batch')->check($param)) {
            return json(['status' => 400, 'msg' => lang_plugins($this->validate->getError())]);
        }

        $result = (new RedeemModel())->generateBatch($param);

        if ((int) $result['status'] === 200) {
            LlmGatewayLogic::audit(
                'LLM网关：生成卡密批次' . $result['data']['batch_no'] . ' 共' . $result['data']['count'] . '张，面额'
                . LlmGatewayLogic::fromMicros($result['data']['value_micros']) . '元 管理员#admin' . get_admin_id(),
                (int) get_admin_id()
            );
        }

        return json($result);
    }

    /**
     * 时间 2026-11-05
     * @title 批次列表
     * @desc 按批次汇总（总数/未用/面额合计）
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/redeem/batch
     * @method GET
     * @param string batch_no - desc:批次号模糊搜索 validate:optional
     * @param int page - desc:页码 validate:optional
     * @param int limit - desc:每页条数 validate:optional
     */
    public function batchList()
    {
        return json((new RedeemModel())->batchList($this->request->param()));
    }

    /**
     * 时间 2026-11-05
     * @title 卡密明细
     * @desc 某批次下的卡密（**不含明文**，只有前缀）
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/redeem/codes
     * @method GET
     * @param string batch_no - desc:批次号 validate:required
     * @param int page - desc:页码 validate:optional
     * @param int limit - desc:每页条数 validate:optional
     */
    public function codeList()
    {
        return json((new RedeemModel())->codeList($this->request->param()));
    }

    /**
     * 时间 2026-11-05
     * @title 批次启用 / 禁用
     * @desc 禁用后该批未兑换的卡密不可兑换
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/redeem/batch/status
     * @method PUT
     * @param string batch_no - desc:批次号 validate:required
     * @param int status - desc:1启用 0禁用 validate:required
     */
    public function setBatchStatus()
    {
        $param = $this->request->param();
        $batchNo = trim((string) ($param['batch_no'] ?? ''));
        $status = (int) ($param['status'] ?? -1);

        if ($batchNo === '' || !in_array($status, [0, 1], true)) {
            return json(['status' => 400, 'msg' => lang_plugins('param_error')]);
        }

        $result = (new RedeemModel())->setBatchStatus($batchNo, $status);

        if ((int) $result['status'] === 200) {
            LlmGatewayLogic::audit(
                'LLM网关：' . ($status === 1 ? '启用' : '禁用') . '卡密批次' . $batchNo . ' 管理员#admin' . get_admin_id(),
                (int) get_admin_id()
            );
        }

        return json($result);
    }

    /**
     * 时间 2026-11-05
     * @title 兑换流水
     * @desc 全站卡密兑换单据（本地 _txn）
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/redeem/txn
     * @method GET
     * @param int client_id - desc:客户ID过滤 validate:optional
     * @param string status - desc:pending|done|failed validate:optional
     * @param int page - desc:页码 validate:optional
     * @param int limit - desc:每页条数 validate:optional
     */
    public function txnList()
    {
        $param = $this->request->param();
        $page = max(1, (int) ($param['page'] ?? 1));
        $limit = min(200, max(1, (int) ($param['limit'] ?? 20)));

        $build = function () use ($param) {
            $q = TxnModel::where('kind', 'redeem_code');
            $clientId = (int) ($param['client_id'] ?? 0);
            if ($clientId > 0) {
                $q->where('client_id', $clientId);
            }
            $status = trim((string) ($param['status'] ?? ''));
            if ($status !== '') {
                $q->where('status', $status);
            }

            return $q;
        };

        $total = (int) $build()->count();
        $list = $build()
            ->field('id,client_id,kind,amount_micros,status,ref_no,remark,create_time,update_time')
            ->order('id', 'desc')
            ->page($page, $limit)
            ->select()
            ->toArray();

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['list' => $list, 'count' => $total],
        ]);
    }
}
