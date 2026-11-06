<?php
namespace addon\idcsmart_llm_gateway\model;

use think\Model;

/**
 * Key 镜像模型
 *
 * 表：idcsmart_addon_idcsmart_llm_gateway_key（$name 前面由 V10 自动补 idcsmart_ 前缀，
 * 与官方 idcsmart_ticket 的约定一致）。
 *
 * 说明：网关是 Key 的事实来源，本表只是本地索引，用于后台检索/封禁/审计与"产品删除时吊销"。
 * **绝不保存 Key 明文**，只保留前缀（prefix）。
 *
 * @author  CZAPI
 * @version 2.0.0
 */
class KeyModel extends Model
{
    protected $name = 'addon_idcsmart_llm_gateway_key';

    # 设置字段信息
    protected $schema = [
        'id'           => 'int',
        'key_id'       => 'int',
        'client_id'    => 'int',
        'host_id'      => 'int',
        'name'         => 'string',
        'prefix'       => 'string',
        'status'       => 'string',
        'models'       => 'string',
        'ip_whitelist' => 'string',
        'expires_at'   => 'int',
        'created_at'   => 'int',
        'update_time'  => 'int',
    ];

    /**
     * 用网关返回的 Key 列表刷新本地镜像（按 key_id upsert）
     *
     * ★ host_id 的处理有过一次事故：网关早期不返回 host_id，本方法无条件
     *   用 $item['host_id'] 覆盖本地值，于是每刷新一次列表，所有 Key 的
     *   host_id 就被清零一次——按产品筛选永远为空，删除产品时一把 Key
     *   也吊销不到。现在改为：网关给了就用网关的，没给（或给 0）就保留
     *   本地已有的值，绝不主动把它抹掉。
     *
     * @param int   $clientId
     * @param array $items 网关 /keys 的 items 数组
     * @return int 处理条数
     */
    public static function syncFromGateway($clientId, array $items)
    {
        $clientId = (int) $clientId;
        $count = 0;
        $now = time();

        foreach ($items as $item) {
            if (empty($item['key_id'])) {
                continue;
            }

            $keyId = (int) $item['key_id'];
            $exist = self::where('key_id', $keyId)->find();

            $gatewayHostId = (int) ($item['host_id'] ?? 0);
            if ($gatewayHostId <= 0 && !empty($exist)) {
                # 网关没给，保留本地已知归属。
                $gatewayHostId = (int) ($exist['host_id'] ?? 0);
            }

            $row = [
                'client_id'    => $clientId,
                'host_id'      => $gatewayHostId,
                'name'         => (string) ($item['name'] ?? ''),
                'prefix'       => (string) ($item['key_prefix'] ?? ''),
                'status'       => (string) ($item['status'] ?? 'active'),
                'models'       => json_encode($item['models'] ?? [], JSON_UNESCAPED_UNICODE),
                'ip_whitelist' => json_encode($item['ip_whitelist'] ?? [], JSON_UNESCAPED_UNICODE),
                'expires_at'   => (int) ($item['expires_at'] ?? 0),
                'created_at'   => (int) ($item['created_at'] ?? 0),
                'update_time'  => $now,
            ];

            if (empty($exist)) {
                self::create(array_merge($row, ['key_id' => $keyId]));
            } else {
                $exist->save($row);
            }
            $count++;
        }

        return $count;
    }

    /**
     * 按客户 + 产品取 Key 镜像
     *
     * @param int $clientId
     * @param int $hostId
     * @return array
     */
    public static function listByHost($clientId, $hostId)
    {
        return self::where('client_id', (int) $clientId)
            ->where('host_id', (int) $hostId)
            ->select()
            ->toArray();
    }

    /**
     * 本地 Key 数量（用于 max_keys_per_host 本地兜底）
     *
     * @param int $clientId
     * @param int $hostId
     * @return int
     */
    public static function countByHost($clientId, $hostId)
    {
        return (int) self::where('client_id', (int) $clientId)
            ->where('host_id', (int) $hostId)
            ->where('status', '<>', 'revoked')
            ->count();
    }
}
