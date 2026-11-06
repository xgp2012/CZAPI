<?php

namespace addon\idcsmart_llm_gateway\validate;

use think\Validate;

/**
 * 卡密校验器
 *
 * 卡密字母表为 23456789ABCDEFGHJKLMNPQRSTUVWXYZ（32 字符，20 位）——
 * 校验时统一转大写，允许用户粘贴带空格/短横线的串（控制器里 trim + strtoupper）。
 *
 * @title 卡密校验
 * @description 接口说明:卡密兑换/批次生成参数校验
 */
class RedeemValidate extends Validate
{
    protected $rule = [
        'code'        => 'require|length:20|alphaNum',
        'count'       => 'require|integer|between:1,5000',
        'amount_yuan' => 'require|float|gt:0',
        'expires_at'  => 'integer|egt:0',
        'batch_no'    => 'max:64',
    ];

    protected $message = [
        'code.require'    => 'llm_gw_redeem_code_require',
        'code.length'     => 'llm_gw_redeem_code_length',
        'code.alphaNum'   => 'llm_gw_redeem_code_format',
        'count.require'   => 'llm_gw_batch_count_require',
        'count.integer'   => 'llm_gw_batch_count_invalid',
        'count.between'   => 'llm_gw_batch_count_invalid',
        'amount_yuan.require' => 'llm_gw_amount_require',
        'amount_yuan.float'   => 'llm_gw_amount_invalid',
        'amount_yuan.gt'      => 'llm_gw_amount_invalid',
        'expires_at.integer'  => 'llm_gw_expires_at_invalid',
        'expires_at.egt'      => 'llm_gw_expires_at_invalid',
        'batch_no.max'        => 'llm_gw_batch_no_max',
    ];

    protected $scene = [
        'redeem' => ['code'],
        'batch'  => ['count', 'amount_yuan', 'expires_at', 'batch_no'],
    ];
}
