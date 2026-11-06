-- 0001_init.sql — CZAPI gateway schema (plan §7) + V10 host-usage view helpers.
-- Up: creates every cz_* table with idempotency guarantees relied on by the
-- billing engine. Down: drops them all.
--
-- Conventions: micro-yuan integers (1 yuan = 1_000_000), quota in tokens,
-- UTF-8/utf8mb4, InnoDB. Usage table is partitioned monthly by created_at.

-- ---------------------------------------------------------------------------
-- 0001_up
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS cz_site (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  app_id      VARCHAR(64)  NOT NULL,
  name        VARCHAR(128) NOT NULL DEFAULT '',
  secret      VARBINARY(255) NOT NULL,            -- sealed with master key
  secret_prev VARBINARY(255) NULL,
  ip_allow    JSON NULL,
  enabled     TINYINT NOT NULL DEFAULT 1,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_app_id (app_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_account (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  v10_site_id    VARCHAR(64)  NOT NULL,
  v10_client_id  BIGINT UNSIGNED NOT NULL,
  email          VARCHAR(190) NULL,
  status         TINYINT NOT NULL DEFAULT 1,
  balance_credit BIGINT NOT NULL DEFAULT 0,
  allow_negative TINYINT NOT NULL DEFAULT 0,
  created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_site_client (v10_site_id, v10_client_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_plan (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  plan_code     VARCHAR(64) NOT NULL,
  name          VARCHAR(128) NOT NULL,
  quota_tokens  BIGINT NOT NULL,
  period        ENUM('month','quarter','year','once') NOT NULL DEFAULT 'month',
  rpm_limit     INT NOT NULL DEFAULT 60,
  tpm_limit     INT NOT NULL DEFAULT 100000,
  concurrency   INT NOT NULL DEFAULT 5,
  model_group   VARCHAR(64) NOT NULL DEFAULT 'default',
  max_keys      INT NOT NULL DEFAULT 5,
  price_cents   BIGINT NOT NULL DEFAULT 0,
  group_ratio   DECIMAL(10,4) NOT NULL DEFAULT 1.0000,
  status        TINYINT NOT NULL DEFAULT 1,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_plan_code (plan_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_subscription (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id   BIGINT UNSIGNED NOT NULL,
  v10_host_id  BIGINT UNSIGNED NOT NULL,
  plan_id      BIGINT UNSIGNED NOT NULL,
  period_start DATETIME NOT NULL,
  period_end   DATETIME NOT NULL,
  quota_total  BIGINT NOT NULL,
  quota_used   BIGINT NOT NULL DEFAULT 0,
  billing_mode ENUM('subscription','balance','hybrid') NOT NULL DEFAULT 'hybrid',
  status       ENUM('active','suspended','expired','terminated') NOT NULL DEFAULT 'active',
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_host (v10_host_id),
  KEY idx_account (account_id),
  KEY idx_status_end (status, period_end)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_api_key (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id      BIGINT UNSIGNED NOT NULL,
  subscription_id BIGINT UNSIGNED NULL,
  key_hash        CHAR(64) NOT NULL,
  key_prefix      VARCHAR(20) NOT NULL,
  name            VARCHAR(128) NOT NULL DEFAULT '',
  models          JSON NULL,
  ip_whitelist    JSON NULL,
  expires_at      DATETIME NULL,
  status          ENUM('active','suspended','revoked') NOT NULL DEFAULT 'active',
  last_used_at    DATETIME NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_hash (key_hash),
  KEY idx_account (account_id),
  KEY idx_sub (subscription_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_channel (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name            VARCHAR(128) NOT NULL,
  provider        VARCHAR(32) NOT NULL DEFAULT 'openai',
  base_url        VARCHAR(255) NOT NULL,
  key_pool        JSON NOT NULL,
  models          JSON NOT NULL,
  model_map       JSON NULL,
  weight          INT NOT NULL DEFAULT 100,
  priority        INT NOT NULL DEFAULT 0,
  max_concurrency INT NOT NULL DEFAULT 100,
  timeout_ms      INT NOT NULL DEFAULT 300000,
  status          TINYINT NOT NULL DEFAULT 1,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_channel_health (
  channel_id  BIGINT UNSIGNED NOT NULL,
  open_until  DATETIME NULL,
  half_open   TINYINT NOT NULL DEFAULT 0,
  error_rate  DECIMAL(6,4) NOT NULL DEFAULT 0,
  samples     INT NOT NULL DEFAULT 0,
  updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (channel_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_model (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  model_name       VARCHAR(128) NOT NULL,
  display_name     VARCHAR(128) NOT NULL,
  group_name       VARCHAR(64) NOT NULL DEFAULT 'default',
  context_len      INT NOT NULL DEFAULT 0,
  model_ratio      DECIMAL(10,4) NOT NULL DEFAULT 1.0000,
  completion_ratio DECIMAL(10,4) NOT NULL DEFAULT 1.0000,
  cached_ratio     DECIMAL(10,4) NOT NULL DEFAULT 0.5000,
  input_micros     BIGINT NOT NULL DEFAULT 0,
  output_micros    BIGINT NOT NULL DEFAULT 0,
  capabilities     JSON NULL,
  status           TINYINT NOT NULL DEFAULT 1,
  created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_model (model_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_usage_record (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id        CHAR(26) NOT NULL,
  account_id        BIGINT UNSIGNED NOT NULL,
  subscription_id   BIGINT UNSIGNED NULL,
  api_key_id        BIGINT UNSIGNED NOT NULL,
  v10_host_id       BIGINT UNSIGNED NULL,
  model             VARCHAR(128) NOT NULL,
  channel_id        BIGINT UNSIGNED NOT NULL,
  prompt_tokens     INT NOT NULL DEFAULT 0,
  completion_tokens INT NOT NULL DEFAULT 0,
  cached_tokens     INT NOT NULL DEFAULT 0,
  cost_micros       BIGINT NOT NULL DEFAULT 0,
  billing_mode      TINYINT NOT NULL DEFAULT 0,
  estimated         TINYINT NOT NULL DEFAULT 0,
  partial           TINYINT NOT NULL DEFAULT 0,
  shadow            TINYINT NOT NULL DEFAULT 0,
  stream            TINYINT NOT NULL DEFAULT 0,
  latency_ms        INT NOT NULL DEFAULT 0,
  ttft_ms           INT NOT NULL DEFAULT 0,
  status_code       SMALLINT NOT NULL DEFAULT 0,
  error_code        VARCHAR(64) NULL,
  created_at        DATETIME NOT NULL,
  PRIMARY KEY (id, created_at),
  UNIQUE KEY uk_request (request_id, created_at),
  KEY idx_key_time (api_key_id, created_at),
  KEY idx_host_time (v10_host_id, created_at),
  KEY idx_account_time (account_id, created_at),
  KEY idx_shadow_time (shadow, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
PARTITION BY RANGE COLUMNS(created_at) (
  PARTITION p2026m11 VALUES LESS THAN ('2026-12-01'),
  PARTITION p2026m12 VALUES LESS THAN ('2027-01-01'),
  PARTITION p2027m01 VALUES LESS THAN ('2027-02-01'),
  PARTITION p2027m02 VALUES LESS THAN ('2027-03-01'),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
);

CREATE TABLE IF NOT EXISTS cz_credit_ledger (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id    BIGINT UNSIGNED NOT NULL,
  type          ENUM('v10_order','redeem_code','admin_adjust','refund','consume') NOT NULL,
  amount_micros BIGINT NOT NULL,
  balance_after BIGINT NOT NULL,
  ref_type      VARCHAR(32) NOT NULL,
  ref_id        VARCHAR(64) NOT NULL,
  remark        VARCHAR(255) NULL,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_account_time (account_id, created_at),
  UNIQUE KEY uk_ref (ref_type, ref_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_admin_user (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  username      VARCHAR(64) NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  role          VARCHAR(32) NOT NULL DEFAULT 'admin',
  status        TINYINT NOT NULL DEFAULT 1,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_audit_log (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  actor_type  VARCHAR(16) NOT NULL,
  actor_id    VARCHAR(64) NOT NULL DEFAULT '',
  action      VARCHAR(64) NOT NULL,
  object_type VARCHAR(32) NOT NULL DEFAULT '',
  object_id   VARCHAR(64) NOT NULL DEFAULT '',
  before_json TEXT NULL,
  after_json  TEXT NULL,
  ip          VARCHAR(64) NOT NULL DEFAULT '',
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_flag (
  flag_key   VARCHAR(64) NOT NULL,
  scope      VARCHAR(64) NOT NULL DEFAULT '',
  flag_value TINYINT NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (flag_key, scope)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_daily_usage (
  day               DATE NOT NULL,
  account_id        BIGINT UNSIGNED NOT NULL,
  v10_host_id       BIGINT UNSIGNED NOT NULL DEFAULT 0,
  api_key_id        BIGINT UNSIGNED NOT NULL DEFAULT 0,
  model             VARCHAR(128) NOT NULL DEFAULT '',
  requests          BIGINT NOT NULL DEFAULT 0,
  prompt_tokens     BIGINT NOT NULL DEFAULT 0,
  completion_tokens BIGINT NOT NULL DEFAULT 0,
  cost_micros       BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (day, account_id, v10_host_id, api_key_id, model),
  KEY idx_day (day)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS cz_nonce (
  nonce      VARCHAR(64) NOT NULL,
  app_id     VARCHAR(64) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (nonce),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ---------------------------------------------------------------------------
-- 0001_down
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS cz_nonce;
DROP TABLE IF EXISTS cz_daily_usage;
DROP TABLE IF EXISTS cz_flag;
DROP TABLE IF EXISTS cz_audit_log;
DROP TABLE IF EXISTS cz_admin_user;
DROP TABLE IF EXISTS cz_credit_ledger;
DROP TABLE IF EXISTS cz_usage_record;
DROP TABLE IF EXISTS cz_model;
DROP TABLE IF EXISTS cz_channel_health;
DROP TABLE IF EXISTS cz_channel;
DROP TABLE IF EXISTS cz_api_key;
DROP TABLE IF EXISTS cz_subscription;
DROP TABLE IF EXISTS cz_plan;
DROP TABLE IF EXISTS cz_account;
DROP TABLE IF EXISTS cz_site;
