<?php
namespace addon\idcsmart_llm_gateway\controller\clientarea;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use addon\idcsmart_llm_gateway\model\LlmApi;
use addon\idcsmart_llm_gateway\model\RedeemModel;
use addon\idcsmart_llm_gateway\model\TxnModel;
use addon\idcsmart_llm_gateway\validate\RedeemValidate;
use app\event\controller\PluginBaseController;
use think\facade\Cache;

/**
 * 前台（会员中心）卡密兑换
 *
 * plan §10.2 的落地：
 *   1. 每人每日 5 次失败上限（防爆破）
 *   2. 事务内用 `UPDATE ... WHERE redeemed_at IS NULL` 做并发闸门（同一张卡不可能被兑换两次）
 *   3. 成功后写 _txn 单据，再调网关 /internal/v1/credit（ref_type=redeem_code, ref_id=卡密ID）
 *   4. 网关暂时不可达 -> 不回滚，返回"处理中，稍后自动重试"，由 _pushlog 重投收尾
 *
 * @title LLM网关卡密兑换(会员中心)
 * @desc LLM网关卡密兑换(会员中心)
 * @use addon\idcsmart_llm_gateway\controller\clientarea\RedeemController
 */
class RedeemController extends PluginBaseController
{
    # 每人每日失败上限
    const MAX_FAIL_PER_DAY = 5;

    # 失败计数缓存键前缀
    const FAIL_KEY_PREFIX = 'llm_gw_redeem_fail_';

    private $validate = null;

    public function initialize()
    {
        parent::initialize();
        $this->validate = new RedeemValidate();
    }

    /**
     * 时间 2026-11-05
     * @title 卡密兑换
     * @desc 兑换卡密到账户余额（网关侧加额度）
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/redeem
     * @method POST
     * @param string code - desc:20位卡密 validate:required
     * @return int amount_micros - desc:到账金额，微元
     * @return int balance_micros - desc:到账后余额
     */
    public function redeem()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $param = $this->request->param();
        if (!$this->validate->scene('redeem')->check($param)) {
            return json(['status' => 400, 'msg' => lang_plugins($this->validate->getError())]);
        }

        # 1) 失败次数闸门（防爆破）
        if ($this->failCount((int) $clientId) >= self::MAX_FAIL_PER_DAY) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_redeem_too_many_attempts')]);
        }

        $code = strtoupper(trim((string) $param['code']));
        $hash = RedeemModel::hashCode($code);

        # 2) 预检（只读，不占锁）：给出精确的失败原因
        $card = RedeemModel::where('code_hash', $hash)->find();
        if (empty($card)) {
            $this->addFail((int) $clientId);
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_redeem_code_invalid')]);
        }
        if ((int) $card['status'] !== 1) {
            $this->addFail((int) $clientId);
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_redeem_batch_disabled')]);
        }
        if (!empty($card['redeemed_at'])) {
            $this->addFail((int) $clientId);
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_redeem_code_used')]);
        }
        if ((int) $card['expires_at'] > 0 && (int) $card['expires_at'] < time()) {
            $this->addFail((int) $clientId);
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_redeem_code_expired')]);
        }

        $cardId = (int) $card['id'];
        $amountMicros = (int) $card['value_micros'];
        $now = time();

        /*
         * 3) 并发闸门：一条 UPDATE ... WHERE redeemed_at IS NULL。
         *    单条 UPDATE 在 InnoDB 下天然原子，affected=1 表示"我抢到了这张卡"，
         *    affected=0 表示已被别人（或并发的另一个请求）先兑换 —— 直接判失败。
         *    这里不用 SELECT ... FOR UPDATE，是为了少一次往返与少一把锁。
         */
        $affected = RedeemModel::where('id', $cardId)
            ->whereNull('redeemed_at')
            ->update([
                'redeemed_by' => (int) $clientId,
                'redeemed_at' => $now,
                'update_time' => $now,
            ]);

        if ((int) $affected !== 1) {
            $this->addFail((int) $clientId);
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_redeem_code_used')]);
        }

        # 4) 本地单据（幂等第一道闸门）。理论上不可能重复，但保留兜底判断。
        try {
            $claim = TxnModel::claim(
                (int) $clientId,
                'redeem_code',
                $amountMicros,
                (string) $cardId,
                '卡密兑换 批次' . (string) $card['batch_no']
            );
        } catch (\Throwable $e) {
            # 本地写单据失败：把卡退回未兑换状态（真正的"回滚"），并提示稍后重试
            RedeemModel::where('id', $cardId)->update([
                'redeemed_by' => 0,
                'redeemed_at' => null,
                'update_time' => time(),
            ]);

            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_redeem_processing')]);
        }

        if (!$claim['created']) {
            TxnModel::markStatus($claim['id'], 'done');
            return json([
                'status' => 400,
                'msg'    => lang_plugins('llm_gw_redeem_code_used'),
            ]);
        }

        # 5) 调网关加额度。ref_type / ref_id 必填（契约 §2.3），幂等第二道闸门在网关侧。
        $res = LlmApi::creditAccount(
            (int) $clientId,
            'redeem_code',
            $amountMicros,
            'redeem_code',
            (string) $cardId,
            'V10 卡密兑换，卡密ID ' . $cardId
        );

        $verdict = LlmApi::classify($res);

        if ((int) $res['code'] === LlmApi::CODE_OK) {
            TxnModel::markStatus($claim['id'], 'done');
            $this->clearFail((int) $clientId);

            $balance = 0;
            if (is_array($res['data']) && isset($res['data']['balance_micros'])) {
                $balance = (int) $res['data']['balance_micros'];
            }

            LlmGatewayLogic::audit(
                'LLM网关：卡密兑换成功 客户#client' . $clientId . ' 卡密#' . $cardId . ' 金额#' . $amountMicros,
                $cardId
            );

            return json([
                'status' => 200,
                'msg'    => lang_plugins('success_message'),
                'data'   => [
                    'amount_micros'  => $amountMicros,
                    'balance_micros' => $balance,
                    'applied'        => (bool) ($res['data']['applied'] ?? true),
                ],
            ]);
        }

        if ($verdict === 'retry') {
            /*
             * 网关暂时不可达（超时/5xx/429）：
             * **不回滚**。卡已被占用、_txn 已落库、_pushlog 已是 failed 待重投，
             * 定时任务会把这次加额补上。回滚反而会让同一张卡被再兑换一次。
             */
            TxnModel::markStatus($claim['id'], 'pending');

            return json([
                'status' => 202,
                'msg'    => lang_plugins('llm_gw_redeem_processing'),
                'data'   => [
                    'amount_micros' => $amountMicros,
                    'pending'       => true,
                ],
            ]);
        }

        # 4xx（除 429）等确定性失败：网关明确拒绝，单据置 failed，提示联系客服
        TxnModel::markStatus($claim['id'], 'failed');
        $this->addFail((int) $clientId);

        return json([
            'status' => 400,
            'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_redeem_failed'),
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 兑换记录
     * @desc 当前客户的卡密兑换流水（来自本地 _txn）
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/redeem/records
     * @method GET
     * @param int page - desc:页码 validate:optional
     * @param int limit - desc:每页条数 validate:optional
     * @return array list - desc:记录
     * @return int count - desc:总数
     */
    public function records()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $page = max(1, (int) $this->request->param('page', 1));
        $limit = min(100, max(1, (int) $this->request->param('limit', 20)));

        $total = (int) TxnModel::where('client_id', (int) $clientId)
            ->where('kind', 'redeem_code')
            ->count();

        $list = TxnModel::field('id,amount_micros,status,ref_no,remark,create_time')
            ->where('client_id', (int) $clientId)
            ->where('kind', 'redeem_code')
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

    /* ==================================================================
     * 失败计数（每人每日 5 次）
     * ==================================================================
     * // ⚠️ 待实例验证: 计数放在 V10 的 Cache（ThinkPHP Cache facade）里，键含日期，TTL 到当日 24 点。
     * //   选择 Cache 而不是新建第 6 张表，是为了严格保持任务书要求的 5 张表结构。
     * //   若目标实例未配置可用缓存驱动（或跨进程不共享），该限流会退化为"基本无效"，
     * //   需要在实例上确认 cache.driver（file/redis）后再决定是否迁移到独立表。
     */

    /**
     * 读取今日失败次数
     *
     * @param int $clientId
     * @return int
     */
    private function failCount($clientId)
    {
        try {
            return (int) Cache::get($this->failKey($clientId), 0);
        } catch (\Throwable $e) {
            return 0;
        }
    }

    /**
     * 失败次数 +1
     *
     * @param int $clientId
     * @return void
     */
    private function addFail($clientId)
    {
        try {
            $key = $this->failKey($clientId);
            $count = (int) Cache::get($key, 0) + 1;
            $ttl = max(60, strtotime('tomorrow') - time());
            Cache::set($key, $count, $ttl);
        } catch (\Throwable $e) {
            # 缓存不可用时不阻断兑换主流程，但会在 README 的待验证项里标注
        }
    }

    /**
     * 成功后清空失败计数
     *
     * @param int $clientId
     * @return void
     */
    private function clearFail($clientId)
    {
        try {
            Cache::delete($this->failKey($clientId));
        } catch (\Throwable $e) {
            # 忽略
        }
    }

    /**
     * 失败计数缓存键
     *
     * @param int $clientId
     * @return string
     */
    private function failKey($clientId)
    {
        return self::FAIL_KEY_PREFIX . (int) $clientId . '_' . date('Ymd');
    }
}
