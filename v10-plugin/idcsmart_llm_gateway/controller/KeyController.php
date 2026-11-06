<?php
namespace addon\idcsmart_llm_gateway\controller;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use addon\idcsmart_llm_gateway\model\KeyModel;
use addon\idcsmart_llm_gateway\model\LlmApi;
use app\event\controller\PluginAdminBaseController;

/**
 * 后台 Key 检索 / 封禁
 *
 * V10 后台对 Key 只做"看与封禁"（plan §8.1 注释 / 附录 C）；重配置在网关 Vue3 后台。
 *
 * @title LLM网关Key(后台)
 * @desc LLM网关Key(后台)
 * @use addon\idcsmart_llm_gateway\controller\KeyController
 */
class KeyController extends PluginAdminBaseController
{
    /**
     * 时间 2026-11-05
     * @title Key 列表（本地镜像）
     * @desc 支持按客户ID / 产品ID / 状态 / 前缀检索
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/keys
     * @method GET
     * @param int client_id - desc:客户ID validate:optional
     * @param int host_id - desc:产品ID validate:optional
     * @param string status - desc:active|suspended|revoked validate:optional
     * @param string keyword - desc:名称或前缀 validate:optional
     * @param int page - desc:页码 validate:optional
     * @param int limit - desc:每页条数 validate:optional
     * @return array list - desc:Key列表
     * @return int count - desc:总数
     */
    public function keyList()
    {
        $param = $this->request->param();
        $page = max(1, (int) ($param['page'] ?? 1));
        $limit = min(200, max(1, (int) ($param['limit'] ?? 20)));

        $build = function () use ($param) {
            $q = KeyModel::where('id', '>', 0);

            $clientId = (int) ($param['client_id'] ?? 0);
            if ($clientId > 0) {
                $q->where('client_id', $clientId);
            }
            $hostId = (int) ($param['host_id'] ?? 0);
            if ($hostId > 0) {
                $q->where('host_id', $hostId);
            }
            $status = trim((string) ($param['status'] ?? ''));
            if ($status !== '') {
                $q->where('status', $status);
            }
            $keyword = trim((string) ($param['keyword'] ?? ''));
            if ($keyword !== '') {
                $q->where(function ($sub) use ($keyword) {
                    $sub->where('name', 'like', '%' . $keyword . '%')
                        ->whereOr('prefix', 'like', '%' . $keyword . '%');
                });
            }

            return $q;
        };

        $total = (int) $build()->count();
        $list = $build()
            ->field('id,key_id,client_id,host_id,name,prefix,status,models,ip_whitelist,expires_at,created_at')
            ->order('id', 'desc')
            ->page($page, $limit)
            ->select()
            ->toArray();

        # models / ip_whitelist 存的是 JSON 字符串，这里解成数组给前端用
        foreach ($list as &$row) {
            $row['models'] = json_decode((string) $row['models'], true) ?: [];
            $row['ip_whitelist'] = json_decode((string) $row['ip_whitelist'], true) ?: [];
        }

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['list' => $list, 'count' => $total],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 从网关拉取 Key（刷新本地镜像）
     * @desc 按客户ID从网关 /internal/v10/keys 拉全量并刷新本地镜像
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/keys/sync
     * @method POST
     * @param int client_id - desc:客户ID validate:required
     */
    public function sync()
    {
        $clientId = (int) $this->request->param('client_id', 0);
        if ($clientId <= 0) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_client_id_require')]);
        }

        $res = LlmApi::listKeys($clientId);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
            ]);
        }

        $items = $res['data']['items'] ?? [];
        $count = KeyModel::syncFromGateway($clientId, $items);

        LlmGatewayLogic::audit('LLM网关：刷新客户#client' . $clientId . ' 的Key镜像，共' . $count . '条', $clientId);

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['count' => $count],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 封禁 / 解封 Key
     * @desc 调网关 PATCH /internal/v10/keys/{id}，同时更新本地镜像
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/keys/:id/status
     * @method PUT
     * @param int id - desc:Key ID(网关侧) validate:required
     * @param string status - desc:active|suspended validate:required
     */
    public function updateStatus()
    {
        $param = $this->request->param();
        $keyId = (int) ($param['id'] ?? 0);
        $status = (string) ($param['status'] ?? '');

        if ($keyId <= 0) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_key_id_require')]);
        }
        if (!in_array($status, ['active', 'suspended'], true)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_key_status_in')]);
        }

        $row = KeyModel::where('key_id', $keyId)->find();
        if (empty($row)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_key_not_exist')]);
        }

        $res = LlmApi::updateKey((int) $row['client_id'], $keyId, ['status' => $status]);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_key_update_failed'),
            ]);
        }

        $row->save(['status' => $status, 'update_time' => time()]);

        LlmGatewayLogic::audit(
            'LLM网关：' . ($status === 'suspended' ? '封禁' : '解封') . 'Key #' . $keyId . ' 管理员#admin' . get_admin_id(),
            $keyId
        );

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['key_id' => $keyId, 'status' => $status],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 吊销 Key
     * @desc 调网关 DELETE /internal/v10/keys/{id}
     * @author CZAPI
     * @version v1
     * @url /admin/v1/llm/keys/:id
     * @method DELETE
     * @param int id - desc:Key ID(网关侧) validate:required
     */
    public function delete()
    {
        $keyId = (int) $this->request->param('id', 0);
        if ($keyId <= 0) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_key_id_require')]);
        }

        $row = KeyModel::where('key_id', $keyId)->find();
        if (empty($row)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_key_not_exist')]);
        }

        $res = LlmApi::deleteKey((int) $row['client_id'], $keyId);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_key_delete_failed'),
            ]);
        }

        if (!empty($row)) {
            $row->save(['status' => 'revoked', 'update_time' => time()]);
        }

        LlmGatewayLogic::audit('LLM网关：后台吊销Key #' . $keyId . ' 管理员#admin' . get_admin_id(), $keyId);

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['key_id' => $keyId, 'status' => 'revoked'],
        ]);
    }
}
