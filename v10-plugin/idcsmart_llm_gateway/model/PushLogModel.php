<?php
namespace addon\idcsmart_llm_gateway\model;

use think\Model;

/**
 * 推送队列模型（可靠投递，plan §10.3）
 *
 * 表：idcsmart_addon_idcsmart_llm_gateway_pushlog
 *
 * 投递语义（与 docs/internal-api-v1.md §1.4 的重试策略一一对应）：
 *   HTTP 200 且 code=0        -> done
 *   4xx（429 除外）           -> done（把差异记进 last_error，不重试）
 *   429 / 5xx / 超时 / 网络错 -> failed，指数退避 1,2,4,8,16,30,30… 分钟，最多 12 次
 *   code=40102（nonce 重放）  -> done（网关已视为处理过）
 *
 * @author  CZAPI
 * @version 2.0.0
 */
class PushLogModel extends Model
{
    protected $name = 'addon_idcsmart_llm_gateway_pushlog';

    # 设置字段信息
    protected $schema = [
        'id'            => 'int',
        'endpoint'      => 'string',
        'payload'       => 'string',
        'ref_type'      => 'string',
        'ref_id'        => 'string',
        'attempts'      => 'int',
        'next_retry_at' => 'int',
        'status'        => 'string',
        'last_error'    => 'string',
        'create_time'   => 'int',
        'update_time'   => 'int',
    ];

    # 最多重试次数（超过后置 failed 并告警）
    const MAX_ATTEMPTS = 12;

    /**
     * 写一条待投递记录（**必须在调用网关之前落库**，保证不丢）
     *
     * @param string $endpoint 如 /internal/v10/credit
     * @param array  $payload  请求体
     * @param string $refType
     * @param string $refId
     * @return int pushlog id
     */
    public static function enqueue($endpoint, array $payload, $refType, $refId)
    {
        $now = time();
        $model = self::create([
            'endpoint'      => (string) $endpoint,
            'payload'       => json_encode($payload, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES),
            'ref_type'      => (string) $refType,
            'ref_id'        => (string) $refId,
            'attempts'      => 0,
            'next_retry_at' => 0,
            'status'        => 'pending',
            'last_error'    => '',
            'create_time'   => $now,
            'update_time'   => $now,
        ]);

        return (int) $model['id'];
    }

    /**
     * 退避秒数：1, 2, 4, 8, 16, 30, 30… 分钟
     *
     * @param int $attempts 已尝试次数（第 1 次失败时为 1）
     * @return int 秒
     */
    public static function backoffSeconds($attempts)
    {
        $attempts = max(1, (int) $attempts);
        $minutes = 1 << min($attempts - 1, 5); // 1,2,4,8,16,32 -> 后面统一封顶 30
        if ($minutes > 30) {
            $minutes = 30;
        }

        return $minutes * 60;
    }

    /**
     * 标记投递成功
     *
     * @param int    $id
     * @param string $note 可选的差异说明（例如 4xx 但已按 done 处理）
     * @return void
     */
    public static function markDone($id, $note = '')
    {
        $row = self::find((int) $id);
        if (empty($row)) {
            return;
        }

        $row->save([
            'status'        => 'done',
            'attempts'      => (int) $row['attempts'] + 1,
            'next_retry_at' => 0,
            'last_error'    => (string) $note,
            'update_time'   => time(),
        ]);
    }

    /**
     * 标记失败并安排下次重试
     *
     * @param int    $id
     * @param string $error
     * @param bool   $terminal 是否终止重试（如 503 功能开关关闭）
     * @return void
     */
    public static function markFailed($id, $error, $terminal = false)
    {
        $row = self::find((int) $id);
        if (empty($row)) {
            return;
        }

        $attempts = (int) $row['attempts'] + 1;
        $exhausted = $attempts >= self::MAX_ATTEMPTS;

        $row->save([
            'status'        => 'failed',
            'attempts'      => $attempts,
            'next_retry_at' => ($terminal || $exhausted) ? 0 : time() + self::backoffSeconds($attempts),
            'last_error'    => mb_substr((string) $error, 0, 900),
            'update_time'   => time(),
        ]);
    }

    /**
     * 取出待重投的记录（next_retry_at <= now 或 pending 且无 next_retry_at）
     *
     * @param int $limit
     * @return array
     */
    public static function dueList($limit = 50)
    {
        $now = time();

        return self::where('status', 'failed')
            ->where('next_retry_at', '>', 0)
            ->where('next_retry_at', '<=', $now)
            ->order('id', 'asc')
            ->limit((int) $limit)
            ->select()
            ->toArray();
    }

    /**
     * 队列分页（后台可视化 + 手动重投）
     *
     * @param array $param ['status','page','limit','ref_id']
     * @return array
     */
    public function queueList(array $param)
    {
        $page = max(1, (int) ($param['page'] ?? 1));
        $limit = min(200, max(1, (int) ($param['limit'] ?? 20)));

        $build = function () use ($param) {
            $q = $this->where('id', '>', 0);
            $status = trim((string) ($param['status'] ?? ''));
            if ($status !== '') {
                $q->where('status', $status);
            }
            $refId = trim((string) ($param['ref_id'] ?? ''));
            if ($refId !== '') {
                $q->where('ref_id', 'like', '%' . $refId . '%');
            }

            return $q;
        };

        $total = (int) $build()->count();
        $list = $build()
            ->field('id,endpoint,ref_type,ref_id,attempts,next_retry_at,status,last_error,create_time,update_time')
            ->order('id', 'desc')
            ->page($page, $limit)
            ->select()
            ->toArray();

        return [
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['list' => $list, 'count' => $total],
        ];
    }
}
