CREATE TABLE IF NOT EXISTS claim_side_effects (
  side_effect_id BIGINT NOT NULL AUTO_INCREMENT,
  claim_id BIGINT NOT NULL,
  effect_type VARCHAR(64) NOT NULL,
  payload_json JSON NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
  retry_count INT NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  last_error VARCHAR(255) NOT NULL DEFAULT '',
  async_task_id BIGINT NULL,
  outbox_event_id BIGINT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (side_effect_id),
  UNIQUE KEY uk_claim_effect (claim_id, effect_type),
  KEY idx_side_effect_dispatch (status, next_retry_at, side_effect_id),
  CONSTRAINT fk_side_effect_claim FOREIGN KEY (claim_id) REFERENCES coupon_claims(claim_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
