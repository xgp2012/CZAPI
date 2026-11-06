<?php
namespace addon\idcsmart_llm_gateway\model;

use think\Model;

/**
 * 用量日聚合模型（V10 侧展示缓存）
 *
 * 表：idcsmart_addon_idcsmart_llm_gateway_usage
 * 唯一键：uk_key_day(key_id, day)
 *
 * 网关是计费与用量的事实来源；本表只是把 /internal/v10/usage/summary 的结果按天落一份，
 * 让 V10 后台在网关不可达时仍能看到历史快照（退化只读，不参与扣费）。
 *
 * @author  CZAPI
 * @version 2.0.0
 */
class UsageModel extends Model
{
    protected $name = 'addon_idcsmart_llm_gateway_usage';

    # 设置字段信息
    protected $schema = [
        'id'                => 'int',
        'client_id'         => 'int',
        'host_id'           => 'int',
        'key_id'            => 'int',
        'day'               => 'string',
        'requests'          => 'int',
        'prompt_tokens'     => 'int',
        'completion_tokens' => 'int',
        'cost_micros'       => 'int',
        'create_time'       => 'int',
        'update_time'       => 'int',
    ];

    /**
     * upsert 一天的快照（按 key_id + day 幂等）
     *
     * @param int    $clientId
     * @param int    $hostId
     * @param int    $keyId
     * @param string $day      YYYY-MM-DD
     * @param array  $bucket   ['requests','prompt_tokens','completion_tokens','cost_micros']
     * @return void
     */
    public static function upsertDay($clientId, $hostId, $keyId, $day, array $bucket)
    {
        $now = time();
        $data = [
            'client_id'         => (int) $clientId,
            'host_id'           => (int) $hostId,
            'requests'          => (int) ($bucket['requests'] ?? 0),
            'prompt_tokens'     => (int) ($bucket['prompt_tokens'] ?? 0),
            'completion_tokens' => (int) ($bucket['completion_tokens'] ?? 0),
            'cost_micros'       => (int) ($bucket['cost_micros'] ?? 0),
            'update_time'       => $now,
        ];

        $exist = self::where('client_id', (int) $clientId)
            ->where('host_id', (int) $hostId)
            ->where('key_id', (int) $keyId)
            ->where('day', (string) $day)
            ->find();
        if (empty($exist)) {
            self::create(array_merge($data, [
                'key_id'      => (int) $keyId,
                'day'         => (string) $day,
                'create_time' => $now,
            ]));
        } else {
            $exist->save($data);
        }
    }

    /**
     * 客户维度的日聚合（网关不可达时的降级数据源）
     *
     * 字段名刻意与网关 /usage/summary 的 buckets 对齐，
     * 这样前端拿到的是同一套键，不必为"实时/降级"写两套渲染逻辑。
     *
     * @param int $clientId
     * @param int $fromTs
     * @param int $toTs
     * @return array
     */
    public static function dailyByClient($clientId, $fromTs, $toTs)
    {
        $fromDay = date('Y-m-d', (int) $fromTs);
        $toDay = date('Y-m-d', (int) $toTs);

        $rows = self::field('day,SUM(requests) AS requests,SUM(prompt_tokens) AS prompt_tokens,'
                . 'SUM(completion_tokens) AS completion_tokens,SUM(cost_micros) AS cost_micros')
            ->where('client_id', (int) $clientId)
            ->whereBetween('day', [$fromDay, $toDay])
            ->group('day')
            ->order('day', 'asc')
            ->select()
            ->toArray();

        foreach ($rows as &$row) {
            $costMicros = (int) $row['cost_micros'];
            $row['key'] = (string) $row['day'];
            $row['request_count'] = (int) $row['requests'];
            $row['prompt_tokens'] = (int) $row['prompt_tokens'];
            $row['completion_tokens'] = (int) $row['completion_tokens'];
            $row['quota_micros'] = $costMicros;
            $row['quota_amount'] = number_format($costMicros / 1000000, 6, '.', '');
            unset($row['requests'], $row['cost_micros']);
        }

        return $rows;
    }

    /**
     * 客户维度合计（网关不可达时的降级数据源）
     *
     * @param int $clientId
     * @param int $fromTs
     * @param int $toTs
     * @return array
     */
    public static function totalByClient($clientId, $fromTs, $toTs)
    {
        $fromDay = date('Y-m-d', (int) $fromTs);
        $toDay = date('Y-m-d', (int) $toTs);

        $row = self::field('SUM(requests) AS requests,SUM(prompt_tokens) AS prompt_tokens,'
                . 'SUM(completion_tokens) AS completion_tokens,SUM(cost_micros) AS cost_micros')
            ->where('client_id', (int) $clientId)
            ->whereBetween('day', [$fromDay, $toDay])
            ->find();

        $costMicros = (int) ($row['cost_micros'] ?? 0);

        return [
            'request_count'     => (int) ($row['requests'] ?? 0),
            'quota_micros'      => $costMicros,
            'quota_amount'      => number_format($costMicros / 1000000, 6, '.', ''),
            'prompt_tokens'     => (int) ($row['prompt_tokens'] ?? 0),
            'completion_tokens' => (int) ($row['completion_tokens'] ?? 0),
        ];
    }
}
