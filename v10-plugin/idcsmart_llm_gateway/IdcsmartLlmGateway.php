<?php
namespace addon\idcsmart_llm_gateway;

use addon\idcsmart_llm_gateway\logic\LlmGatewayLogic;
use addon\idcsmart_llm_gateway\model\LlmApi;
use app\common\lib\Plugin;
use think\facade\Db;

/**
 * CZAPI —— 魔方业务系统 V10 LLM 网关插件（主类）
 *
 * 约定（源码实证，见 docs/v10-notes.md）：
 *   - 目录名 = 插件标识（小写下划线）；类名 = 目录名大驼峰；namespace = addon\{目录名}
 *   - 必须 extends app\common\lib\Plugin
 *   - install() 里直接跑 CREATE TABLE（官方插件没有 install.sql 约定）
 *   - 表名前缀 idcsmart_addon_{插件目录名}_*
 *
 * @author  CZAPI
 * @version 2.1.0
 */
class IdcsmartLlmGateway extends Plugin
{
    # 插件基本信息
    public $info = [
        'name'        => 'IdcsmartLlmGateway', // 插件英文名，唯一标识
        'title'       => 'LLM 网关',            // 插件名称
        'description' => '在魔方业务系统内管理 LLM API Key、模型与用量', // 插件描述
        'author'      => 'CZAPI',               // 开发者
        'version'     => '2.1.0',               // 版本号
    ];

    # 五张业务表的表名后缀（完整表名 = idcsmart_addon_idcsmart_llm_gateway_ + 后缀）
    const TABLE_SUFFIXES = ['key', 'redeem', 'txn', 'pushlog', 'usage'];

    /**
     * 插件安装
     *
     * 直接执行 CREATE TABLE：官方插件（idcsmart_ticket 4.2.2）就是在 install() 里逐条 Db::execute，
     * 不存在 install.sql 约定（plan 附录 D #7）。这里用 IF NOT EXISTS，便于重复安装/升级时幂等。
     *
     * @return bool
     */
    public function install()
    {
        $sql = $this->schemaSql();

        foreach ($sql as $statement) {
            Db::execute($statement);
        }

        # 定时任务 Hook 需要登记到 idcsmart_plugin_hook（源码实证）
        $this->registerCronHooks();

        return true;
    }

    /**
     * 登记定时任务类 Hook
     *
     * 为什么需要这一步（源码实证）：官方 idcsmart_ticket 4.2.2 的 upgrade() 里有
     *   INSERT INTO `idcsmart_plugin_hook`(`name`,`status`,`plugin`,`module`,`order`)
     *   VALUES ('five_minute_cron',1,'IdcsmartTicket','addon',0);
     * 说明 **cron 类 hook 必须先在本表登记**，系统的定时任务才会按插件逐个回调。
     * 普通业务 hook（如 order_paid、after_host_delete）由 V10 的事件分发器按 hooks.php / 主类方法
     * 动态解析，无需登记。
     *
     * 幂等：先按 (name, plugin, module) 查重再插入；整段落 try/catch，登记失败不影响插件安装
     * （大不了定时任务不跑，用户仍可在插件后台「推送队列」页手动重投）。
     *
     * // ⚠️ 待实例验证: 本表的列集合取自官方插件的 INSERT 语句（name/status/plugin/module/order），
     * //   若目标实例的表结构有额外必填列，本条 INSERT 会抛异常——已被捕获并记录，不会阻断安装。
     * //   上线后到后台「插件 → Hook」列表确认两条 cron 已登记；若无，手工执行 README 里给的 SQL。
     *
     * @return void
     */
    private function registerCronHooks()
    {
        $pluginName = 'IdcsmartLlmGateway';
        $hooks = ['daily_cron', 'five_minute_cron'];

        foreach ($hooks as $hookName) {
            try {
                $exist = Db::name('plugin_hook')
                    ->where('name', $hookName)
                    ->where('plugin', $pluginName)
                    ->where('module', 'addon')
                    ->find();

                if (!empty($exist)) {
                    # 已登记但可能被停用，重新打开
                    if ((int) ($exist['status'] ?? 0) !== 1) {
                        Db::name('plugin_hook')->where('id', (int) $exist['id'])->update(['status' => 1]);
                    }
                    continue;
                }

                Db::name('plugin_hook')->insert([
                    'name'   => $hookName,
                    'status' => 1,
                    'plugin' => $pluginName,
                    'module' => 'addon',
                    'order'  => 0,
                ]);
            } catch (\Throwable $e) {
                # 表不存在 / 列不匹配 / 无写权限：不阻断安装，留给运维按 README 手工登记
                continue;
            }
        }
    }

    /**
     * 注销定时任务 Hook 登记
     *
     * 卸载后必须清掉，否则系统的定时任务仍会尝试回调一个已经不存在的插件类。
     * 与 keep_data_on_uninstall 无关：这属于"配置/注册信息"，不是业务数据。
     *
     * @return void
     */
    private function unregisterCronHooks()
    {
        try {
            Db::name('plugin_hook')
                ->where('plugin', 'IdcsmartLlmGateway')
                ->where('module', 'addon')
                ->whereIn('name', ['daily_cron', 'five_minute_cron'])
                ->delete();
        } catch (\Throwable $e) {
            # 忽略：V10 卸载流程通常也会清理
        }
    }

    /**
     * 插件卸载（默认保守）
     *
     * 依据 plan §8.2：默认只删配置与导航，**保留业务数据**（Key / 卡密 / 流水 / 推送 / 用量）。
     * 彻底卸载（连表一起删）由后台「彻底卸载」开关触发：先写入 keep_data_on_uninstall=0 再卸载，
     * 或直接调用 dropTables()。见 upgrade.php / controller/RedeemController.php 的说明。
     *
     * @return bool
     */
    public function uninstall()
    {
        # 先注销 cron hook 登记（与是否保留业务数据无关）
        $this->unregisterCronHooks();

        # V10 会自行清理 idcsmart_plugin / idcsmart_nav 中属于本插件的记录；
        # 这里只做插件自己能确定要做的事：按开关决定是否删表。
        $keepData = LlmGatewayLogic::config('keep_data_on_uninstall', '1');
        $keepData = (string) $keepData === '0' ? false : true;

        if (!$keepData) {
            $this->dropTables();
        }

        return true;
    }

    /**
     * 插件升级
     *
     * 官方插件在 upgrade() 中用 version_compare 判断逐版本补丁。本插件 1.0.0 为首版，
     * 保留该结构供后续版本追加；当前只做"缺表补建"的幂等兜底。
     *
     * @return bool
     */
    public function upgrade()
    {
        foreach ($this->schemaSql() as $statement) {
            Db::execute($statement);
        }

        $this->addMissingColumns();

        return true;
    }

    /**
     * 给已存在的表补列
     *
     * 为什么必须单独做：schemaSql() 全是 CREATE TABLE IF NOT EXISTS，
     * 表一旦存在就整体跳过——**新增的列永远不会被加上**。已安装的实例升级后
     * 仍缺列，随后每次写该表都会抛 "Unknown column"，表现为"后台一打开就报错"。
     *
     * 这里逐列探测再补，全程 try/catch：补列失败不阻断升级，
     * 但会在系统日志里留下一条可检索的记录。
     *
     * @return void
     */
    private function addMissingColumns()
    {
        # 表后缀 => [列名 => 列定义]
        $columns = [
            'pushlog' => [
                # 重投时按原 HTTP 方法发送，不能一律当 POST。
                'method' => "`method` varchar(8) NOT NULL DEFAULT 'POST' COMMENT '重投时使用的HTTP方法'",
            ],
        ];

        foreach ($columns as $suffix => $defs) {
            $table = 'idcsmart_addon_idcsmart_llm_gateway_' . $suffix;

            foreach ($defs as $column => $definition) {
                try {
                    # 用参数绑定而不是自己拼引号：Db::quote 在部分 ThinkPHP 版本里不存在。
                    $exists = Db::query("SHOW COLUMNS FROM `{$table}` LIKE ?", [$column]);
                    if (!empty($exists)) {
                        continue;
                    }

                    Db::execute("ALTER TABLE `{$table}` ADD COLUMN {$definition}");
                } catch (\Throwable $e) {
                    try {
                        active_log(
                            '[LLM网关] 补列失败 ' . $table . '.' . $column . '：' . $e->getMessage(),
                            'llm_gateway',
                            0
                        );
                    } catch (\Throwable $ignore) {
                        # 日志本身失败就放弃，不能因此中断升级流程。
                    }
                }
            }
        }
    }

    /**
     * 彻底卸载：删除全部业务表（不可恢复）
     *
     * 单独暴露为方法而不是放进 uninstall()，是为了让"彻底卸载"必须被显式调用一次，
     * 避免运维误操作把客户 Key 与卡密流水一起删掉。
     *
     * @return bool
     */
    public function dropTables()
    {
        foreach (self::TABLE_SUFFIXES as $suffix) {
            Db::execute("DROP TABLE IF EXISTS `idcsmart_addon_idcsmart_llm_gateway_{$suffix}`;");
        }

        return true;
    }

    /**
     * 定时任务（主类小驼峰形式）
     *
     * 官方 idcsmart_ticket 4.2.2 就是这样承接 hook 的：
     *   public function fiveMinuteCron($param) { ... }
     * 与 hooks.php 里的 add_hook('five_minute_cron', ...) 是同一事件的两条注册路径。
     * 两条都保留：hooks.php 便于集中管理，主类方法覆盖"实例只认主类方法"的情况。
     * 重复触发是安全的——retryFailed() 只处理 status=pending/failed 且到期的记录，
     * 已 done 的不会重复投递（网关侧还有 (ref_type, ref_id) 幂等索引兜底）。
     *
     * @param mixed $param ['config'=>array, 'output'=>object]
     * @return void
     */
    public function fiveMinuteCron($param)
    {
        $report = LlmApi::retryFailed(50);

        if (is_array($param) && isset($param['output']) && is_object($param['output'])) {
            if ((int) $report['scanned'] > 0) {
                $param['output']->writeln(
                    '[LLM网关] 5 分钟任务：补投扫描 ' . (int) $report['scanned']
                    . ' 条，成功 ' . (int) $report['done']
                    . ' 条，失败 ' . (int) $report['failed'] . ' 条'
                );
            }
        }
    }

    /**
     * 每日任务（主类小驼峰形式，与 hooks.php 的 daily_cron 对应）
     *
     * @param mixed $param ['config'=>array, 'output'=>object]
     * @return void
     */
    public function dailyCron($param)
    {
        $report = LlmApi::dailySync();

        if (is_array($param) && isset($param['output']) && is_object($param['output'])) {
            $param['output']->writeln(
                '[LLM网关] 每日任务：补投扫描 ' . (int) ($report['retry']['scanned'] ?? 0)
                . ' 条，成功 ' . (int) ($report['retry']['done'] ?? 0)
                . ' 条，失败 ' . (int) ($report['retry']['failed'] ?? 0)
                . ' 条；用量快照 ' . (int) ($report['usage_keys'] ?? 0) . ' 条'
            );
        }
    }

    /**
     * 建表语句集合（install / upgrade 共用，必须幂等）
     *
     * @return string[]
     */
    public function schemaSql()
    {
        return [
            # ---------------------------------------------------------------
            # Key 镜像表：V10 侧对网关 Key 的本地索引（便于后台检索/封禁/审计）
            # ---------------------------------------------------------------
            "CREATE TABLE IF NOT EXISTS `idcsmart_addon_idcsmart_llm_gateway_key` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `key_id` int(11) NOT NULL DEFAULT '0' COMMENT '网关侧Key ID',
  `client_id` int(11) NOT NULL DEFAULT '0' COMMENT 'V10客户ID',
  `host_id` int(11) NOT NULL DEFAULT '0' COMMENT 'V10产品ID',
  `name` varchar(191) NOT NULL DEFAULT '' COMMENT 'Key名称',
  `prefix` varchar(32) NOT NULL DEFAULT '' COMMENT 'Key前缀，明文不再保存',
  `status` varchar(16) NOT NULL DEFAULT 'active' COMMENT 'active|suspended|revoked',
  `models` text COMMENT '模型白名单JSON数组',
  `ip_whitelist` text COMMENT 'IP白名单JSON数组',
  `expires_at` int(11) NOT NULL DEFAULT '0' COMMENT '过期时间戳，0不过期',
  `created_at` int(11) NOT NULL DEFAULT '0' COMMENT '创建时间',
  `update_time` int(11) NOT NULL DEFAULT '0' COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_key_id` (`key_id`),
  KEY `idx_client_id` (`client_id`) USING BTREE,
  KEY `idx_host_id` (`host_id`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC COMMENT='LLM网关Key镜像表';",

            # ---------------------------------------------------------------
            # 卡密表：只存 sha256(code) + 前缀 + 批次，不存明文
            # ---------------------------------------------------------------
            "CREATE TABLE IF NOT EXISTS `idcsmart_addon_idcsmart_llm_gateway_redeem` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `batch_no` varchar(64) NOT NULL DEFAULT '' COMMENT '批次号',
  `code_hash` char(64) NOT NULL DEFAULT '' COMMENT 'sha256(卡密明文)',
  `code_prefix` varchar(16) NOT NULL DEFAULT '' COMMENT '卡密前缀，便于客服核对',
  `value_micros` bigint(20) NOT NULL DEFAULT '0' COMMENT '面额，微元（1元=1000000）',
  `redeemed_by` int(11) NOT NULL DEFAULT '0' COMMENT '兑换客户ID',
  `redeemed_at` int(11) DEFAULT NULL COMMENT '兑换时间，NULL=未兑换',
  `expires_at` int(11) NOT NULL DEFAULT '0' COMMENT '卡密过期时间，0不过期',
  `status` tinyint(1) NOT NULL DEFAULT '1' COMMENT '1可用 0整批禁用',
  `create_time` int(11) NOT NULL DEFAULT '0' COMMENT '创建时间',
  `update_time` int(11) NOT NULL DEFAULT '0' COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code_hash` (`code_hash`),
  KEY `idx_batch_no` (`batch_no`) USING BTREE,
  KEY `idx_redeemed_by` (`redeemed_by`) USING BTREE,
  KEY `idx_redeemed_at` (`redeemed_at`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC COMMENT='LLM网关卡密表';",

            # ---------------------------------------------------------------
            # 本地单据表：入账/扣账的幂等凭证（与网关 (ref_type,ref_id) 双重保障）
            # ---------------------------------------------------------------
            "CREATE TABLE IF NOT EXISTS `idcsmart_addon_idcsmart_llm_gateway_txn` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `client_id` int(11) NOT NULL DEFAULT '0' COMMENT 'V10客户ID',
  `kind` varchar(32) NOT NULL DEFAULT '' COMMENT 'v10_order|redeem_code|admin_adjust|refund',
  `amount_micros` bigint(20) NOT NULL DEFAULT '0' COMMENT '微元，正入负出',
  `status` varchar(16) NOT NULL DEFAULT 'pending' COMMENT 'pending|done|failed',
  `ref_no` varchar(64) NOT NULL DEFAULT '' COMMENT '业务单号（订单号/卡密ID/调整单号）',
  `remark` varchar(255) NOT NULL DEFAULT '' COMMENT '备注',
  `create_time` int(11) NOT NULL DEFAULT '0' COMMENT '创建时间',
  `update_time` int(11) NOT NULL DEFAULT '0' COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_kind_ref` (`kind`,`ref_no`),
  KEY `idx_client_id` (`client_id`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC COMMENT='LLM网关本地单据表';",

            # ---------------------------------------------------------------
            # 推送队列：可靠投递（plan §10.3）
            # ---------------------------------------------------------------
            "CREATE TABLE IF NOT EXISTS `idcsmart_addon_idcsmart_llm_gateway_pushlog` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `endpoint` varchar(128) NOT NULL DEFAULT '' COMMENT '内部接口路径，如 /internal/v10/credit',
  `method` varchar(8) NOT NULL DEFAULT 'POST' COMMENT '重投时使用的HTTP方法',
  `payload` text COMMENT '请求体JSON',
  `ref_type` varchar(32) NOT NULL DEFAULT '' COMMENT '幂等凭证类型',
  `ref_id` varchar(64) NOT NULL DEFAULT '' COMMENT '幂等凭证ID',
  `attempts` int(11) NOT NULL DEFAULT '0' COMMENT '已尝试次数',
  `next_retry_at` int(11) NOT NULL DEFAULT '0' COMMENT '下次重试时间戳，0=不再重试',
  `status` varchar(16) NOT NULL DEFAULT 'pending' COMMENT 'pending|done|failed',
  `last_error` varchar(1000) NOT NULL DEFAULT '' COMMENT '最后一次错误',
  `create_time` int(11) NOT NULL DEFAULT '0' COMMENT '创建时间',
  `update_time` int(11) NOT NULL DEFAULT '0' COMMENT '更新时间',
  PRIMARY KEY (`id`),
  KEY `idx_status_next` (`status`,`next_retry_at`) USING BTREE,
  KEY `idx_ref` (`ref_type`,`ref_id`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC COMMENT='LLM网关推送队列表';",

            # ---------------------------------------------------------------
            # 用量日聚合快照：V10 侧展示用（网关是事实来源，这里只是缓存）
            # ---------------------------------------------------------------
            "CREATE TABLE IF NOT EXISTS `idcsmart_addon_idcsmart_llm_gateway_usage` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `client_id` int(11) NOT NULL DEFAULT '0' COMMENT 'V10客户ID',
  `host_id` int(11) NOT NULL DEFAULT '0' COMMENT 'V10产品ID',
  `key_id` int(11) NOT NULL DEFAULT '0' COMMENT '网关Key ID',
  `day` date NOT NULL DEFAULT '1970-01-01' COMMENT '统计日期',
  `requests` int(11) NOT NULL DEFAULT '0' COMMENT '请求数',
  `prompt_tokens` bigint(20) NOT NULL DEFAULT '0' COMMENT '输入token',
  `completion_tokens` bigint(20) NOT NULL DEFAULT '0' COMMENT '输出token',
  `cost_micros` bigint(20) NOT NULL DEFAULT '0' COMMENT '消费金额，微元',
  `create_time` int(11) NOT NULL DEFAULT '0' COMMENT '创建时间',
  `update_time` int(11) NOT NULL DEFAULT '0' COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_client_host_key_day` (`client_id`,`host_id`,`key_id`,`day`),
  KEY `idx_client_day` (`client_id`,`day`) USING BTREE,
  KEY `idx_host_day` (`host_id`,`day`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC COMMENT='LLM网关用量日聚合表';",
        ];
    }
}
