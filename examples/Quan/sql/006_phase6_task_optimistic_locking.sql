ALTER TABLE async_tasks
  ADD COLUMN version BIGINT NOT NULL DEFAULT 0 AFTER last_error;

CREATE TABLE IF NOT EXISTS task_consume_receipts (
  receipt_id BIGINT NOT NULL AUTO_INCREMENT,
  task_id BIGINT NOT NULL,
  task_type VARCHAR(64) NOT NULL,
  biz_id VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (receipt_id),
  UNIQUE KEY uk_task_consume_receipt (task_type, biz_id),
  UNIQUE KEY uk_task_consume_task_id (task_id),
  CONSTRAINT fk_task_consume_receipt_task FOREIGN KEY (task_id) REFERENCES async_tasks(task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
