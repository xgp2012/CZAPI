<?php
namespace addon\idcsmart_llm_gateway\model;

use think\Model;

/**
 * 本地单据模型（幂等第一道闸门）
 *
 * 表：idcsmart_addon_idcsmart_llm_gateway_txn
 * 唯一键：uk_kind_ref(kind, ref_no)
 *
 * 作用：V10 侧"这笔钱只给网关加一次"的本地凭证。
 * 第二道闸门在网关侧 /internal/v10/credit 的 (ref_type, ref_id) 唯一索引（plan §10.3）。
 *
 * @author  CZAPI
 * @version 2.0.0
 */
class TxnModel extends Model
{
    protected $name = 'addon_idcsmart_llm_gateway_txn';

    # 设置字段信息
    protected $schema = [
        'id'            => 'int',
        'client_id'     => 'int',
        'kind'          => 'string',
        'amount_micros' => 'int',
        'status'        => 'string',
        'ref_no'        => 'string',
        'remark'        => 'string',
        'create_time'   => 'int',
        'update_time'   => 'int',
    ];

    /**
     * 幂等登记一张单据
     *
     * 返回 [「是否新登记」, 单据ID]。已存在时返回 false，调用方**必须**据此跳过加额。
     *
     * @param int    $clientId
     * @param string $kind         v10_order|redeem_code|admin_adjust|refund
     * @param int    $amountMicros
     * @param string $refNo
     * @param string $remark
     * @return array ['created'=>bool,'id'=>int,'row'=>array|null]
     */
    public static function claim($clientId, $kind, $amountMicros, $refNo, $remark = '')
    {
        $kind = (string) $kind;
        $refNo = (string) $refNo;

        $exist = self::where('kind', $kind)->where('ref_no', $refNo)->find();
        if (!empty($exist)) {
            return ['created' => false, 'id' => (int) $exist['id'], 'row' => $exist->toArray()];
        }

        try {
            $now = time();
            $model = self::create([
                'client_id'     => (int) $clientId,
                'kind'          => $kind,
                'amount_micros' => (int) $amountMicros,
                'status'        => 'pending',
                'ref_no'        => $refNo,
                'remark'        => (string) $remark,
                'create_time'   => $now,
                'update_time'   => $now,
            ]);

            return ['created' => true, 'id' => (int) $model['id'], 'row' => $model->toArray()];
        } catch (\Exception $e) {
            # 唯一键冲突 = 并发下别人先登记了，按"已存在"处理（幂等）
            $exist = self::where('kind', $kind)->where('ref_no', $refNo)->find();
            if (!empty($exist)) {
                return ['created' => false, 'id' => (int) $exist['id'], 'row' => $exist->toArray()];
            }

            throw $e;
        }
    }

    /**
     * 更新单据状态
     *
     * @param int    $id
     * @param string $status pending|done|failed
     * @return void
     */
    public static function markStatus($id, $status)
    {
        $row = self::find((int) $id);
        if (!empty($row)) {
            $row->save(['status' => (string) $status, 'update_time' => time()]);
        }
    }
}
