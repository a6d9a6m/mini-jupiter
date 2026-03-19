CREATE TABLE IF NOT EXISTS coupon_campaigns (
  coupon_id BIGINT NOT NULL AUTO_INCREMENT,
  name VARCHAR(128) NOT NULL,
  total_stock INT NOT NULL,
  available_stock INT NOT NULL,
  per_user_limit INT NOT NULL DEFAULT 1,
  status VARCHAR(16) NOT NULL DEFAULT 'DRAFT',
  start_at DATETIME(3) NOT NULL,
  end_at DATETIME(3) NOT NULL,
  version BIGINT NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (coupon_id),
  KEY idx_campaign_status_window (status, start_at, end_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS coupon_claims (
  claim_id BIGINT NOT NULL AUTO_INCREMENT,
  coupon_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'CLAIMED',
  idempotency_key VARCHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (claim_id),
  UNIQUE KEY uk_coupon_user (coupon_id, user_id),
  KEY idx_coupon_user (coupon_id, user_id),
  KEY idx_idempotency_key (idempotency_key),
  CONSTRAINT fk_claim_campaign FOREIGN KEY (coupon_id) REFERENCES coupon_campaigns(coupon_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO coupon_campaigns
  (coupon_id, name, total_stock, available_stock, per_user_limit, status, start_at, end_at)
VALUES
  (1001, 'new_user_100', 1000, 1000, 1, 'ACTIVE', NOW(3) - INTERVAL 1 HOUR, NOW(3) + INTERVAL 7 DAY)
ON DUPLICATE KEY UPDATE
  name = VALUES(name),
  total_stock = VALUES(total_stock),
  available_stock = VALUES(available_stock),
  per_user_limit = VALUES(per_user_limit),
  status = VALUES(status),
  start_at = VALUES(start_at),
  end_at = VALUES(end_at),
  updated_at = CURRENT_TIMESTAMP(3);
