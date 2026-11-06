<?php

namespace addon\idcsmart_llm_gateway\validate;

use think\Validate;

/**
 * Key 校验器
 *
 * 约定与官方插件一致（validate/TicketValidate.php）：$message 里放**语言键**，
 * 由控制器 lang_plugins($this->validate->getError()) 翻译。
 *
 * @title Key校验
 * @description 接口说明:Key 创建/修改参数校验
 */
class KeyValidate extends Validate
{
    protected $rule = [
        'id'            => 'require|integer|gt:0',
        'name'          => 'require|max:100',
        'status'        => 'in:active,suspended',
        'subscription_id' => 'integer|egt:0',
        'host_id'       => 'integer|egt:0',
        'expires_at'    => 'integer|egt:0',
    ];

    protected $message = [
        'id.require'    => 'llm_gw_key_id_require',
        'id.integer'    => 'llm_gw_key_id_invalid',
        'id.gt'         => 'llm_gw_key_id_invalid',
        'name.require'  => 'llm_gw_key_name_require',
        'name.max'      => 'llm_gw_key_name_max',
        'status.in'     => 'llm_gw_key_status_in',
        'expires_at.integer' => 'llm_gw_expires_at_invalid',
        'expires_at.egt'     => 'llm_gw_expires_at_invalid',
    ];

    protected $scene = [
        'create' => ['name', 'subscription_id', 'host_id', 'expires_at'],
        'update' => ['id', 'name', 'status', 'expires_at'],
    ];
}
