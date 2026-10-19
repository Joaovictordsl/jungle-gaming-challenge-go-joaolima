BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wager_transactions WHERE kind='ROLLBACK') THEN
  RAISE EXCEPTION 'cannot revert ROLLBACK migration with rollback history';
 END IF;
END $$;
DROP TRIGGER complete_rollback ON wager_transactions;
DROP FUNCTION check_complete_rollback();
DROP INDEX one_balance_change_per_version;
CREATE UNIQUE INDEX one_balance_change_per_version ON wager_transactions(wallet_id,wallet_version)
 WHERE origin='EXTERNAL' AND status='PROCESSED' AND kind IN ('BET','WIN','REFUND');
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
  OR
  (origin = 'EXTERNAL' AND kind = 'LOSS' AND status = 'PROCESSED'
   AND amount_cents = 0 AND balance_before_cents IS NOT NULL AND balance_before_cents >= 0
   AND balance_after_cents = balance_before_cents AND wallet_version >= 1
   AND failure_code IS NULL AND updated_at >= created_at
   AND provider_id IS NOT NULL AND provider_id !~ '^[[:space:]]*$' AND octet_length(provider_id) <= 256
   AND external_transaction_id IS NOT NULL AND external_transaction_id !~ '^[[:space:]]*$' AND octet_length(external_transaction_id) <= 256
   AND idempotency_key IS NOT NULL AND idempotency_key !~ '^[[:space:]]*$' AND octet_length(idempotency_key) <= 256
   AND payload_hash IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$'
   AND round_id IS NOT NULL AND round_id !~ '^[[:space:]]*$' AND octet_length(round_id) <= 256
   AND game_id IS NOT NULL AND game_id !~ '^[[:space:]]*$' AND octet_length(game_id) <= 256)

  OR
  (origin = 'EXTERNAL' AND kind = 'REFUND' AND amount_cents > 0
   AND provider_id IS NOT NULL AND provider_id !~ '^[[:space:]]*$' AND octet_length(provider_id) <= 256
   AND external_transaction_id IS NOT NULL AND external_transaction_id !~ '^[[:space:]]*$' AND octet_length(external_transaction_id) <= 256
   AND idempotency_key IS NOT NULL AND idempotency_key !~ '^[[:space:]]*$' AND octet_length(idempotency_key) <= 256
   AND payload_hash IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$'
   AND round_id IS NOT NULL AND round_id !~ '^[[:space:]]*$' AND octet_length(round_id) <= 256
   AND game_id IS NOT NULL AND game_id !~ '^[[:space:]]*$' AND octet_length(game_id) <= 256
   AND balance_before_cents IS NOT NULL AND balance_before_cents >= 0 AND balance_after_cents >= 0
   AND updated_at >= created_at
   AND ((status = 'PROCESSED' AND failure_code IS NULL AND wallet_version >= 2
         AND balance_after_cents::NUMERIC = balance_before_cents::NUMERIC + amount_cents::NUMERIC)
     OR (wallet_version >= 1 AND balance_after_cents = balance_before_cents
       AND ((status = 'PENDING_REFERENCE' AND failure_code IS NULL)
         OR (status = 'REJECTED' AND failure_code IS NOT NULL
           AND (failure_code IN ('REFERENCE_MISMATCH','REFERENCE_REJECTED','REFERENCE_NOT_FOUND','REFERENCE_AMOUNT_MISMATCH','ALREADY_REVERSED')
             OR (failure_code = 'BALANCE_OVERFLOW' AND balance_before_cents::NUMERIC + amount_cents::NUMERIC > 9223372036854775807)))))))
);

ALTER TABLE wager_transactions DROP CONSTRAINT win_reference_shape;
ALTER TABLE wager_transactions ADD CONSTRAINT win_reference_shape CHECK (
  (kind NOT IN ('WIN','REFUND') AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL
    AND reference_attempts = 0 AND next_reference_attempt_at IS NULL AND reference_deadline IS NULL)
  OR
  (kind IN ('WIN','REFUND')
   AND (reference_external_transaction_id IS NULL OR (reference_external_transaction_id !~ '^[[:space:]]*$'
     AND octet_length(reference_external_transaction_id) <= 256 AND reference_external_transaction_id <> external_transaction_id))
   AND (reference_transaction_id IS NULL OR reference_external_transaction_id IS NOT NULL)
   AND (reference_deadline IS NULL OR (reference_external_transaction_id IS NOT NULL AND reference_deadline > created_at))
   AND (reference_attempts = 0 OR reference_deadline IS NOT NULL)
   AND ((status = 'PENDING_REFERENCE' AND reference_external_transaction_id IS NOT NULL
         AND reference_transaction_id IS NULL AND next_reference_attempt_at IS NOT NULL AND reference_deadline IS NOT NULL)
     OR (status IN ('PROCESSED', 'REJECTED') AND next_reference_attempt_at IS NULL))
   AND (status <> 'PROCESSED' OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL)
   AND (failure_code IS NULL OR failure_code NOT IN ('REFERENCE_MISMATCH', 'REFERENCE_REJECTED','REFERENCE_AMOUNT_MISMATCH','ALREADY_REVERSED')
     OR (reference_external_transaction_id IS NOT NULL AND reference_transaction_id IS NOT NULL))
   AND (failure_code IS NULL OR failure_code <> 'REFERENCE_NOT_FOUND'
     OR (reference_external_transaction_id IS NOT NULL AND reference_transaction_id IS NULL AND reference_deadline IS NOT NULL))
   AND (failure_code IS NULL OR failure_code <> 'BALANCE_OVERFLOW'
     OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL)
  )
);
ALTER TABLE wager_transactions DROP CONSTRAINT refund_reference_required;
ALTER TABLE wager_transactions ADD CONSTRAINT refund_reference_required CHECK
    (kind <> 'REFUND' OR reference_external_transaction_id IS NOT NULL);
CREATE OR REPLACE FUNCTION protect_bet_ledger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE id = NEW.transaction_id AND origin = 'EXTERNAL'
        AND (kind = 'LOSS' OR status <> 'PROCESSED' OR NEW.direction <> CASE kind WHEN 'BET' THEN 'DEBIT' ELSE 'CREDIT' END
             OR NEW.balance_before_cents <> balance_before_cents OR NEW.balance_after_cents <> balance_after_cents
             OR NEW.created_at <> updated_at)) THEN
        RAISE EXCEPTION 'ledger must match processed transaction' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;


COMMIT;
