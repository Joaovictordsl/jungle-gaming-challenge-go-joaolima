BEGIN;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE kind = 'LOSS') THEN
        RAISE EXCEPTION 'cannot revert LOSS migration with loss history';
    END IF;
END $$;
DROP TRIGGER complete_loss ON wager_transactions;
DROP FUNCTION check_complete_loss();
DROP TRIGGER loss_event_shape ON outbox_events;
DROP FUNCTION protect_loss_event();
DROP INDEX one_balance_change_per_version;
CREATE UNIQUE INDEX one_processed_bet_per_version ON wager_transactions(wallet_id,wallet_version)
    WHERE origin = 'EXTERNAL' AND status = 'PROCESSED';
ALTER TABLE wager_transactions DROP CONSTRAINT transaction_origin_shape;
ALTER TABLE wager_transactions ADD CONSTRAINT transaction_origin_shape CHECK (
  (origin = 'INTERNAL' AND kind = 'OPENING' AND status = 'PROCESSED'
   AND wallet_version = 1 AND balance_after_cents = amount_cents AND updated_at = created_at
   AND provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
   AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
   AND balance_before_cents IS NULL AND failure_code IS NULL)
  OR
  (origin = 'EXTERNAL' AND kind IN ('BET', 'WIN')
   AND provider_id IS NOT NULL AND provider_id !~ '^[[:space:]]*$' AND octet_length(provider_id) <= 256
   AND external_transaction_id IS NOT NULL AND external_transaction_id !~ '^[[:space:]]*$' AND octet_length(external_transaction_id) <= 256
   AND idempotency_key IS NOT NULL AND idempotency_key !~ '^[[:space:]]*$' AND octet_length(idempotency_key) <= 256
   AND payload_hash IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$'
   AND round_id IS NOT NULL AND round_id !~ '^[[:space:]]*$' AND octet_length(round_id) <= 256
   AND game_id IS NOT NULL AND game_id !~ '^[[:space:]]*$' AND octet_length(game_id) <= 256
   AND balance_before_cents IS NOT NULL AND balance_before_cents >= 0 AND balance_after_cents >= 0
   AND updated_at >= created_at
   AND ((status = 'PROCESSED' AND failure_code IS NULL AND wallet_version >= 2
         AND ((kind = 'BET' AND balance_after_cents::NUMERIC = balance_before_cents::NUMERIC - amount_cents::NUMERIC)
           OR (kind = 'WIN' AND balance_after_cents::NUMERIC = balance_before_cents::NUMERIC + amount_cents::NUMERIC)))
     OR (status IN ('REJECTED', 'PENDING_REFERENCE') AND wallet_version >= 1 AND balance_after_cents = balance_before_cents
       AND ((kind = 'BET' AND status = 'REJECTED' AND failure_code IS NOT NULL AND failure_code = 'INSUFFICIENT_FUNDS' AND amount_cents > balance_before_cents)
         OR (kind = 'WIN' AND (
           (status = 'PENDING_REFERENCE' AND failure_code IS NULL)
           OR (status = 'REJECTED' AND failure_code IS NOT NULL AND
             ((failure_code = 'BALANCE_OVERFLOW' AND balance_before_cents::NUMERIC + amount_cents::NUMERIC > 9223372036854775807)
               OR failure_code IN ('REFERENCE_MISMATCH', 'REFERENCE_REJECTED', 'REFERENCE_NOT_FOUND'))))))))
  )
);
ALTER TABLE wager_transactions DROP CONSTRAINT wager_transactions_amount_cents_check;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_transactions_amount_cents_check CHECK (amount_cents > 0);
CREATE OR REPLACE FUNCTION protect_bet_ledger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE id = NEW.transaction_id AND origin = 'EXTERNAL'
        AND (status <> 'PROCESSED' OR NEW.direction <> CASE kind WHEN 'BET' THEN 'DEBIT' ELSE 'CREDIT' END
             OR NEW.balance_before_cents <> balance_before_cents OR NEW.balance_after_cents <> balance_after_cents
             OR NEW.created_at <> updated_at)) THEN
        RAISE EXCEPTION 'ledger must match processed transaction' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

COMMIT;
