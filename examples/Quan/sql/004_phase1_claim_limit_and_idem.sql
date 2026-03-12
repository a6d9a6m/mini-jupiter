ALTER TABLE coupon_claims
  DROP INDEX uk_coupon_user;

ALTER TABLE coupon_claims
  ADD UNIQUE KEY uk_coupon_user_idem (coupon_id, user_id, idempotency_key);
