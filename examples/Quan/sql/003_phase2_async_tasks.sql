CREATE TABLE IF NOT EXISTS async_tasks (
  task_id BIGINT NOT NULL AUTO_INCREMENT,
  task_type VARCHAR(64) NOT NULL,
  biz_id VARCHAR(128) NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
  payload_json JSON NOT NULL,
  retry_count INT NOT NULL DEFAULT 0,
  max_retry INT NOT NULL DEFAULT 5,
  next_retry_at DATETIME(3) NULL,
  last_error VARCHAR(255) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (task_id),
  UNIQUE KEY uk_task_type_biz (task_type, biz_id),
  KEY idx_task_status_next_retry (status, next_retry_at),
  KEY idx_task_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
