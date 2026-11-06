<?php
namespace addon\idcsmart_llm_gateway\controller\clientarea;

use addon\idcsmart_llm_gateway\logic\CallbackGuard;
use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use addon\idcsmart_llm_gateway\model\KeyModel;
use addon\idcsmart_llm_gateway\model\LlmApi;
use addon\idcsmart_llm_gateway\validate\KeyValidate;
use app\event\controller\PluginBaseController;

/**
 * 前台（会员中心）Key 管理
 *
 * 明文只在"创建"这一次返回；之后所有接口只回前缀（KeyModel.prefix）。
 *
 * @title LLM网关Key(会员中心)
 * @desc LLM网关Key(会员中心)
 * @use addon\idcsmart_llm_gateway\controller\clientarea\KeyController
 */
class KeyController extends PluginBaseController
{
    private $validate = null;

    public function initialize()
    {
        parent::initialize();
        $this->validate = new KeyValidate();
    }

    /**
     * 时间 2026-11-05
     * @title Key 列表
     * @desc 当前客户的全部 Key（含 30 天用量），不含明文
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/keys
     * @method GET
     * @param int host_id - desc:可选，按产品过滤
     * @return array list - desc:Key列表
     * @return int count - desc:总数
     */
    public function keyList()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $hostId = (int) $this->request->param('host_id', 0);

        $res = LlmApi::listKeys((int) $clientId, $hostId > 0 ? $hostId : null);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            # 网关不可达时退回本地镜像（只读降级，明确标注 stale）
            $local = KeyModel::where('client_id', (int) $clientId)
                ->when($hostId > 0, function ($q) use ($hostId) {
                    $q->where('host_id', $hostId);
                })
                ->select()
                ->toArray();

            return json([
                'status' => 200,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_gateway_unreachable'),
                'data'   => ['list' => $local, 'count' => count($local), 'stale' => true],
            ]);
        }

        $items = $res['data']['items'] ?? [];

        # 顺手刷新本地镜像，便于后台检索与产品删除时吊销
        KeyModel::syncFromGateway((int) $clientId, $items);

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['list' => $items, 'count' => count($items), 'stale' => false],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 创建 Key
     * @desc 明文只返回这一次，请自行保存
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/keys
     * @method POST
     * @param string name - desc:Key名称 validate:required
     * @param int subscription_id - desc:所属订阅ID validate:optional
     * @param int host_id - desc:所属V10产品ID validate:optional
     * @param array models - desc:模型白名单 validate:optional
     * @param array ip_whitelist - desc:IP白名单 validate:optional
     * @param int expires_at - desc:过期时间戳 validate:optional
     * @return int key_id - desc:Key ID
     * @return string key - desc:Key 明文（仅此一次）
     * @return string key_prefix - desc:Key 前缀
     */
    public function create()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $param = $this->request->param();

        if (!$this->validate->scene('create')->check($param)) {
            return json(['status' => 400, 'msg' => lang_plugins($this->validate->getError())]);
        }

        $hostId = (int) ($param['host_id'] ?? 0);

        # 本地上限兜底（网关侧还有 plan.max_keys）
        $maxKeys = LlmGatewayLogic::configInt('max_keys_per_host', 5);
        if ($hostId > 0 && $maxKeys > 0 && KeyModel::countByHost((int) $clientId, $hostId) >= $maxKeys) {
            return json([
                'status' => 400,
                'msg'    => lang_plugins('llm_gw_key_limit_reached'),
            ]);
        }

        $payload = [
            'client_id' => (int) $clientId,
            'name'      => (string) $param['name'],
        ];
        if ($hostId > 0) {
            $payload['host_id'] = $hostId;
        }
        if (isset($param['models']) && is_array($param['models'])) {
            $payload['models'] = array_values(array_map('strval', $param['models']));
        }
        if (isset($param['ip_whitelist']) && is_array($param['ip_whitelist'])) {
            $payload['ip_whitelist'] = array_values(array_map('strval', $param['ip_whitelist']));
        }
        if (!empty($param['expires_at'])) {
            $payload['expires_at'] = (int) $param['expires_at'];
        }
        if ($maxKeys > 0) {
            # 把本地上限也下发给网关，让网关那一层就拦住超量，而不是只靠本地镜像计数。
            $payload['max_keys'] = $maxKeys;
        }

        $res = LlmApi::createKey($payload);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_key_create_failed'),
                'data'   => null,
            ]);
        }

        $data = is_array($res['data']) ? $res['data'] : [];

        # 写本地镜像（不保存明文）
        if (!empty($data['key_id'])) {
            KeyModel::syncFromGateway((int) $clientId, [[
                'key_id'       => (int) $data['key_id'],
                'key_prefix'   => (string) ($data['key_prefix'] ?? ''),
                'name'         => (string) ($data['name'] ?? $payload['name']),
                'status'       => (string) ($data['status'] ?? 'active'),
                'host_id'      => $hostId,
                'models'       => $payload['models'] ?? [],
                'ip_whitelist' => $payload['ip_whitelist'] ?? [],
                'expires_at'   => $payload['expires_at'] ?? 0,
                'created_at'   => (int) ($data['created_at'] ?? time()),
            ]]);

            LlmGatewayLogic::audit(
                'LLM网关：创建Key #' . $data['key_id'] . ' 客户#client' . $clientId,
                (int) $data['key_id']
            );
        }

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => [
                'key_id'     => (int) ($data['key_id'] ?? 0),
                'key'        => $data['key'] ?? null,   # 明文只此一次；幂等命中时为 null
                'key_prefix' => (string) ($data['key_prefix'] ?? ''),
                'name'       => (string) ($data['name'] ?? ''),
                'status'     => (string) ($data['status'] ?? 'active'),
                'created_at' => (int) ($data['created_at'] ?? time()),
            ],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 修改 Key
     * @desc 改名 / 改模型范围 / 改 IP 白名单 / 启停
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/keys/:id
     * @method PATCH
     * @param int id - desc:Key ID validate:required
     * @param string name - desc:新名称 validate:optional
     * @param string status - desc:active|suspended validate:optional
     * @param array models - desc:模型白名单 validate:optional
     * @param array ip_whitelist - desc:IP白名单 validate:optional
     * @param int expires_at - desc:过期时间戳 validate:optional
     */
    public function update()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $param = $this->request->param();

        if (!$this->validate->scene('update')->check($param)) {
            return json(['status' => 400, 'msg' => lang_plugins($this->validate->getError())]);
        }

        $keyId = (int) $param['id'];

        # 归属校验：本地镜像里不属于该客户的 Key 一律按不存在处理（防越权）
        $owned = KeyModel::where('key_id', $keyId)->where('client_id', (int) $clientId)->find();
        if (empty($owned)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_key_not_exist')]);
        }

        $fields = [];
        foreach (['name', 'status', 'models', 'ip_whitelist', 'expires_at'] as $key) {
            if (array_key_exists($key, $param) && $param[$key] !== '' && $param[$key] !== null) {
                $fields[$key] = $param[$key];
            }
        }
        if (empty($fields)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_no_field_to_update')]);
        }

        $res = LlmApi::updateKey((int) $clientId, $keyId, $fields);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_key_update_failed'),
            ]);
        }

        $data = is_array($res['data']) ? $res['data'] : [];
        KeyModel::syncFromGateway((int) $clientId, [$data]);

        LlmGatewayLogic::audit('LLM网关：修改Key #' . $keyId . ' 客户#client' . $clientId, $keyId);

        return json(['status' => 200, 'msg' => lang_plugins('success_message'), 'data' => $data]);
    }

    /**
     * 时间 2026-11-05
     * @title 吊销 Key
     * @desc 软删除，status=revoked；幂等
     * @author CZAPI
     * @version v1
     * @url /console/v1/llm/keys/:id
     * @method DELETE
     * @param int id - desc:Key ID validate:required
     */
    public function delete()
    {
        $clientId = get_client_id();
        if (empty($clientId)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_not_login')]);
        }

        $keyId = (int) $this->request->param('id', 0);
        if ($keyId <= 0) {
            return json(['status' => 400, 'msg' => lang_plugins('param_error')]);
        }

        $owned = KeyModel::where('key_id', $keyId)->where('client_id', (int) $clientId)->find();
        if (empty($owned)) {
            return json(['status' => 400, 'msg' => lang_plugins('llm_gw_key_not_exist')]);
        }

        $res = LlmApi::deleteKey((int) $clientId, $keyId);
        if ((int) $res['code'] !== LlmApi::CODE_OK) {
            return json([
                'status' => 400,
                'msg'    => $res['msg'] !== '' ? $res['msg'] : lang_plugins('llm_gw_key_delete_failed'),
            ]);
        }

        $owned->save(['status' => 'revoked', 'update_time' => time()]);

        LlmGatewayLogic::audit('LLM网关：吊销Key #' . $keyId . ' 客户#client' . $clientId, $keyId);

        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['key_id' => $keyId, 'status' => 'revoked'],
        ]);
    }

    /**
     * 时间 2026-11-05
     * @title 额度耗尽回调（网关 -> V10，免登录）
     * @desc 属于 console/v1/api 免登录组，安全完全依赖插件自己的 HMAC 校验
     * @author CZAPI
     * @version v1
     * @url /console/v1/api/llm/callback/quota-exhausted
     * @method POST
     * @param int client_id - desc:客户ID validate:required
     * @param int host_id - desc:产品ID validate:optional
     * @param string signature - desc:X-CZ-Signature（也可放请求头）
     */
    public function quotaExhausted()
    {
        /*
         * 免登录组：Check 中间件只表示"不校验登录态"，**不代表安全**（docs/v10-notes.md §8）。
         * 因此这里第一件事是自校验 HMAC。
         *
         * // ⚠️ 待实例验证: 该回调协议为预留（契约 §4 明确"本版不做反向回调"）。
         * //   网关侧尚未实现；callback_secret 未配置时本接口一律拒绝。
         */
        $verify = CallbackGuard::verify($this->request, '/console/v1/api/llm/callback/quota-exhausted');
        if (!$verify['ok']) {
            return json(['status' => 400, 'msg' => $verify['msg']]);
        }

        $param = $this->request->param();
        $clientId = (int) ($param['client_id'] ?? 0);
        $hostId = (int) ($param['host_id'] ?? 0);

        if ($clientId <= 0) {
            return json(['status' => 400, 'msg' => lang_plugins('param_error')]);
        }

        LlmGatewayLogic::audit(
            'LLM网关：网关回调额度耗尽 客户#' . $clientId . ' 产品#' . $hostId,
            $hostId
        );

        # 本版不做自动停机（额度耗尽的权威判定在网关推理链路，返回 402），
        # 这里只落审计 + 留给运维处置，避免误停客户业务。
        return json([
            'status' => 200,
            'msg'    => lang_plugins('success_message'),
            'data'   => ['received' => true],
        ]);
    }
}
