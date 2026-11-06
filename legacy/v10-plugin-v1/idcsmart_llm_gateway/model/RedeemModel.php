<?php
namespace addon\idcsmart_llm_gateway\model;

use think\Model;

/**
 * 卡密模型
 *
 * 表：idcsmart_addon_idcsmart_llm_gateway_redeem
 *
 * 安全约定（plan §10.2）：
 *   - 只保存 sha256(卡密明文) + 前缀 + 批次，**库里永远没有明文**
 *   - 兑换必须用「UPDATE ... WHERE redeemed_at IS NULL」做并发闸门
 *
 * @author  CZAPI
 * @version 1.0.0
 */
class RedeemModel extends Model
{
    protected $name = 'addon_idcsmart_llm_gateway_redeem';

    # 设置字段信息
    protected $schema = [
        'id'           => 'int',
        'batch_no'     => 'string',
        'code_hash'    => 'string',
        'code_prefix'  => 'string',
        'value_micros' => 'int',
        'redeemed_by'  => 'int',
        'redeemed_at'  => 'int',
        'expires_at'   => 'int',
        'status'       => 'int',
        'create_time'  => 'int',
        'update_time'  => 'int',
    ];

    /**
     * 卡密字母表：base32 去掉易混淆的 0 / 1 / I / O，恰好 32 个字符。
     * 20 位 -> 32^20 ≈ 1.27e30 种组合，爆破不可行。
     */
    const ALPHABET = '23456789ABCDEFGHJKLMNPQRSTUVWXYZ';

    const CODE_LENGTH = 20;

    /**
     * 生成一张卡密明文（20 位，crypto 随机）
     *
     * 必须用 random_int：rand/mt_rand 可预测，卡密是可兑换现金的凭证。
     *
     * @return string
     */
    public static function generateCode()
    {
        $alphabet = self::ALPHABET;
        $max = strlen($alphabet) - 1;
        $code = '';
        for ($i = 0; $i < self::CODE_LENGTH; $i++) {
            $code .= $alphabet[random_int(0, $max)];
        }

        return $code;
    }

    /**
     * 卡密明文 -> 查库用 hash
     *
     * 统一转大写、去空白后再哈希，避免用户输入小写导致"卡密不存在"。
     *
     * // ⚠️ 待实例验证: 此处未加盐（salt），是为了支持"用户输入明文直接查库"，
     * //   与 plan §10.2「只存 sha256(code)」一致。若后续要加盐，需给批次表补 salt 字段。
     *
     * @param string $code
     * @return string
     */
    public static function hashCode($code)
    {
        return hash('sha256', strtoupper(trim((string) $code)));
    }

    /**
     * 生产一批卡密
     *
     * @param array $param ['count','amount_yuan','expires_at','batch_no']
     * @return array ['status'=>int,'msg'=>string,'data'=>[...]]
     */
    public function generateBatch(array $param)
    {
        $count = (int) ($param['count'] ?? 0);
        $amountYuan = (float) ($param['amount_yuan'] ?? 0);
        $expiresAt = (int) ($param['expires_at'] ?? 0);
        $batchNo = trim((string) ($param['batch_no'] ?? ''));

        if ($count <= 0 || $count > 5000) {
            return ['status' => 400, 'msg' => lang_plugins('llm_gw_batch_count_invalid')];
        }
        if ($amountYuan <= 0) {
            return ['status' => 400, 'msg' => lang_plugins('llm_gw_amount_invalid')];
        }
        if ($batchNo === '') {
            $batchNo = 'B' . date('YmdHis') . strtoupper(bin2hex(random_bytes(2)));
        }

        $valueMicros = (int) round($amountYuan * 1000000);
        $now = time();
        $codes = [];
        $rows = [];

        $guard = 0;
        while (count($rows) < $count && $guard < $count * 5) {
            $guard++;
            $code = self::generateCode();
            $hash = self::hashCode($code);

            # 碰撞（概率极低）或本批内重复：跳过重来
            if (isset($rows[$hash]) || !empty(self::where('code_hash', $hash)->find())) {
                continue;
            }

            $codes[] = $code;
            $rows[$hash] = [
                'batch_no'     => $batchNo,
                'code_hash'    => $hash,
                'code_prefix'  => substr($code, 0, 4),
                'value_micros' => $valueMicros,
                'redeemed_by'  => 0,
                'redeemed_at'  => null,
                'expires_at'   => $expiresAt,
                'status'       => 1,
                'create_time'  => $now,
                'update_time'  => $now,
            ];
        }

        if (count($rows) < $count) {
            return ['status' => 400, 'msg' => lang_plugins('llm_gw_batch_generate_failed')];
        }

        $this->startTrans();
        try {
            foreach ($rows as $row) {
                $this->insert($row);
            }
            $this->commit();
        } catch (\Exception $e) {
            $this->rollback();
            return ['status' => 400, 'msg' => $e->getMessage()];
        }

        return [
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'batch_no'     => $batchNo,
                'count'        => $count,
                'value_micros' => $valueMicros,
                # 明文只在这一次返回，供导出 CSV 交给渠道
                'codes'        => $codes,
            ],
        ];
    }

    /**
     * 批次汇总列表（按 batch_no 分组）
     *
     * @param array $param ['page','limit','batch_no']
     * @return array
     */
    public function batchList(array $param)
    {
        $page = max(1, (int) ($param['page'] ?? 1));
        $limit = min(200, max(1, (int) ($param['limit'] ?? 20)));
        $batchNo = trim((string) ($param['batch_no'] ?? ''));

        $fields = 'batch_no,COUNT(*) AS total,'
            . 'SUM(CASE WHEN redeemed_at IS NULL THEN 1 ELSE 0 END) AS unused,'
            . 'SUM(value_micros) AS value_micros,MIN(create_time) AS create_time';

        $countQuery = $this->field('batch_no')->group('batch_no');
        if ($batchNo !== '') {
            $countQuery->where('batch_no', 'like', '%' . $batchNo . '%');
        }
        $total = count($countQuery->select()->toArray());

        $listQuery = $this->field($fields)->group('batch_no')->order('create_time', 'desc');
        if ($batchNo !== '') {
            $listQuery->where('batch_no', 'like', '%' . $batchNo . '%');
        }
        $list = $listQuery->page($page, $limit)->select()->toArray();

        return [
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['list' => $list, 'count' => $total],
        ];
    }

    /**
     * 卡密明细列表
     *
     * @param array $param ['batch_no','page','limit','status']
     * @return array
     */
    public function codeList(array $param)
    {
        $page = max(1, (int) ($param['page'] ?? 1));
        $limit = min(200, max(1, (int) ($param['limit'] ?? 20)));
        $batchNo = trim((string) ($param['batch_no'] ?? ''));

        $countQuery = $this->where('id', '>', 0);
        if ($batchNo !== '') {
            $countQuery->where('batch_no', $batchNo);
        }
        $total = (int) $countQuery->count();

        $listQuery = $this->field('id,batch_no,code_prefix,value_micros,redeemed_by,redeemed_at,expires_at,status,create_time')
            ->order('id', 'desc');
        if ($batchNo !== '') {
            $listQuery->where('batch_no', $batchNo);
        }
        $list = $listQuery->page($page, $limit)->select()->toArray();

        return [
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['list' => $list, 'count' => $total],
        ];
    }

    /**
     * 批量禁用/启用一整批（配合"批次禁用"）
     *
     * @param string $batchNo
     * @param int    $status 1 启用 / 0 禁用
     * @return array
     */
    public function setBatchStatus($batchNo, $status)
    {
        $batchNo = trim((string) $batchNo);
        if ($batchNo === '') {
            return ['status' => 400, 'msg' => lang_plugins('param_error')];
        }

        $affected = $this->where('batch_no', $batchNo)->update([
            'status'      => (int) $status === 1 ? 1 : 0,
            'update_time' => time(),
        ]);

        return [
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['affected' => (int) $affected],
        ];
    }
}
