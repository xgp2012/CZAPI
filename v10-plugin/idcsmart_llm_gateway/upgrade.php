<?php
/**
 * 版本升级钩子
 *
 * 官方 idcsmart_ticket 4.2.2 的 upgrade.php 是空文件；真正的升级逻辑写在主类的 upgrade() 里
 * （用 version_compare 判断逐版本补丁）。本插件沿用同样的做法：
 *   - 主类 IdcsmartLlmGateway::upgrade() 负责幂等补建表结构
 *   - 需要数据迁移时，在这里按版本号追加
 *
 * 本文件被系统 include 后应返回 true（或什么都不返回）；此处保持与官方一致的空实现，
 * 迁移逻辑集中放在主类，避免两处逻辑漂移。
 *
 * @author  CZAPI
 * @version 2.0.0
 */

# 1.0.0 首版：无历史版本需要迁移。
# 后续版本示例：
# if (version_compare($fromVersion, '1.1.0', '<')) {
#     Db::execute("ALTER TABLE `idcsmart_addon_idcsmart_llm_gateway_key` ADD COLUMN ...");
# }

return true;
