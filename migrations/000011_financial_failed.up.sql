BEGIN;
-- Amplia os estados sem relaxar as invariantes das operações já existentes.
ALTER TABLE wager_transactions DROP CONSTRAINT transaction_origin_shape;
ALTER TABLE wager_transactions ADD CONSTRAINT transaction_origin_shape CHECK ((
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
 OR
  (origin = 'EXTERNAL' AND kind = 'ROLLBACK' AND amount_cents > 0
   AND provider_id IS NOT NULL AND provider_id !~ '^[[:space:]]*$' AND octet_length(provider_id) <= 256
   AND external_transaction_id IS NOT NULL AND external_transaction_id !~ '^[[:space:]]*$' AND octet_length(external_transaction_id) <= 256
   AND idempotency_key IS NOT NULL AND idempotency_key !~ '^[[:space:]]*$' AND octet_length(idempotency_key) <= 256
   AND payload_hash IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$'
   AND round_id IS NOT NULL AND round_id !~ '^[[:space:]]*$' AND octet_length(round_id) <= 256
   AND game_id IS NOT NULL AND game_id !~ '^[[:space:]]*$' AND octet_length(game_id) <= 256
   AND balance_before_cents IS NOT NULL AND balance_before_cents >= 0 AND balance_after_cents >= 0
   AND updated_at >= created_at
   AND ((status = 'PROCESSED' AND failure_code IS NULL AND wallet_version >= 2
         AND (balance_after_cents::NUMERIC = balance_before_cents::NUMERIC + amount_cents::NUMERIC
 OR balance_after_cents::NUMERIC = balance_before_cents::NUMERIC - amount_cents::NUMERIC))
     OR (wallet_version >= 1 AND balance_after_cents = balance_before_cents
       AND ((status = 'PENDING_REFERENCE' AND failure_code IS NULL)
         OR (status = 'REJECTED' AND failure_code IS NOT NULL
           AND (failure_code IN ('REFERENCE_MISMATCH','REFERENCE_REJECTED','REFERENCE_NOT_FOUND','REFERENCE_AMOUNT_MISMATCH','ALREADY_REVERSED')
             OR (failure_code = 'REVERSAL_INSUFFICIENT_FUNDS' AND amount_cents > balance_before_cents)
 OR (failure_code = 'BALANCE_OVERFLOW' AND balance_before_cents::NUMERIC + amount_cents::NUMERIC > 9223372036854775807)))))))) OR (origin='EXTERNAL' AND kind IN ('BET','WIN','LOSS','REFUND','ROLLBACK') AND status='FAILED'
 AND ((kind='LOSS' AND amount_cents=0) OR (kind<>'LOSS' AND amount_cents>0))
 AND provider_id IS NOT NULL AND provider_id !~ '^[[:space:]]*$' AND octet_length(provider_id)<=256
 AND external_transaction_id IS NOT NULL AND external_transaction_id !~ '^[[:space:]]*$' AND octet_length(external_transaction_id)<=256
 AND idempotency_key IS NOT NULL AND idempotency_key !~ '^[[:space:]]*$' AND octet_length(idempotency_key)<=256
 AND payload_hash IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$'
 AND round_id IS NOT NULL AND round_id !~ '^[[:space:]]*$' AND octet_length(round_id)<=256
 AND game_id IS NOT NULL AND game_id !~ '^[[:space:]]*$' AND octet_length(game_id)<=256
 AND balance_before_cents IS NOT NULL AND balance_before_cents>=0 AND balance_after_cents=balance_before_cents
 AND wallet_version>=1 AND updated_at>=created_at AND failure_code IS NOT NULL
 AND failure_code='INFRASTRUCTURE_RETRIES_EXHAUSTED'
 AND reference_transaction_id IS NULL AND reference_attempts=0 AND reference_deadline IS NULL AND next_reference_attempt_at IS NULL)
);
ALTER TABLE wager_transactions DROP CONSTRAINT win_reference_shape;
ALTER TABLE wager_transactions ADD CONSTRAINT win_reference_shape CHECK (
  (kind NOT IN ('WIN','REFUND','ROLLBACK') AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL
    AND reference_attempts = 0 AND next_reference_attempt_at IS NULL AND reference_deadline IS NULL)
  OR
  (kind IN ('WIN','REFUND','ROLLBACK')
   AND (reference_external_transaction_id IS NULL OR (reference_external_transaction_id !~ '^[[:space:]]*$'
     AND octet_length(reference_external_transaction_id) <= 256 AND reference_external_transaction_id <> external_transaction_id))
   AND (reference_transaction_id IS NULL OR reference_external_transaction_id IS NOT NULL)
   AND (reference_deadline IS NULL OR (reference_external_transaction_id IS NOT NULL AND reference_deadline > created_at))
   AND (reference_attempts = 0 OR reference_deadline IS NOT NULL)
   AND ((status = 'PENDING_REFERENCE' AND reference_external_transaction_id IS NOT NULL
         AND reference_transaction_id IS NULL AND next_reference_attempt_at IS NOT NULL AND reference_deadline IS NOT NULL)
     OR (status IN ('PROCESSED', 'REJECTED', 'FAILED') AND next_reference_attempt_at IS NULL))
   AND (status <> 'PROCESSED' OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL)
   AND (failure_code IS NULL OR failure_code NOT IN ('REFERENCE_MISMATCH', 'REFERENCE_REJECTED','REFERENCE_AMOUNT_MISMATCH','ALREADY_REVERSED','REVERSAL_INSUFFICIENT_FUNDS')
     OR (reference_external_transaction_id IS NOT NULL AND reference_transaction_id IS NOT NULL))
   AND (failure_code IS NULL OR failure_code <> 'REFERENCE_NOT_FOUND'
     OR (reference_external_transaction_id IS NOT NULL AND reference_transaction_id IS NULL AND reference_deadline IS NOT NULL))
   AND (failure_code IS NULL OR failure_code <> 'BALANCE_OVERFLOW'
     OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL)
  )
);
ALTER TABLE inbox_messages DROP CONSTRAINT inbox_shape;
ALTER TABLE inbox_messages ADD CONSTRAINT inbox_shape CHECK ((
  (status='PENDING' AND completed_at IS NULL AND transaction_id IS NULL AND result_status IS NULL
   AND result_failure_code IS NULL AND balance_cents IS NULL AND currency IS NULL AND wallet_version IS NULL AND financial_replay IS NULL AND failure_code IS NULL)
  OR (status='COMPLETED' AND completed_at IS NOT NULL AND completed_at>=received_at AND transaction_id IS NOT NULL
   AND result_status IS NOT NULL AND result_status IN ('PROCESSED','REJECTED','PENDING_REFERENCE')
   AND ((result_status='REJECTED' AND result_failure_code IS NOT NULL) OR (result_status<>'REJECTED' AND result_failure_code IS NULL))
   AND balance_cents IS NOT NULL AND balance_cents>=0 AND currency IS NOT NULL AND currency IN ('BRL','USD')
   AND wallet_version IS NOT NULL AND wallet_version>=1 AND financial_replay IS NOT NULL AND failure_code IS NULL)
  OR (status='FAILED' AND completed_at IS NOT NULL AND completed_at>=received_at AND failure_code IS NOT NULL
   AND transaction_id IS NULL AND result_status IS NULL AND result_failure_code IS NULL AND balance_cents IS NULL AND currency IS NULL AND wallet_version IS NULL AND financial_replay IS NULL)) OR (status='FAILED' AND completed_at IS NOT NULL AND completed_at>=received_at AND failure_code='attempts_exhausted'
 AND transaction_id IS NOT NULL AND result_status IS NOT NULL AND result_status='FAILED'
 AND result_failure_code IS NOT NULL AND result_failure_code='INFRASTRUCTURE_RETRIES_EXHAUSTED'
 AND balance_cents IS NOT NULL AND balance_cents>=0 AND currency IS NOT NULL AND currency IN ('BRL','USD')
 AND wallet_version IS NOT NULL AND wallet_version>=1 AND financial_replay IS NOT NULL)
);

DROP TRIGGER complete_bet ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_bet AFTER INSERT ON wager_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN((NEW.origin='EXTERNAL' AND NEW.kind='BET') AND NEW.status<>'FAILED') EXECUTE FUNCTION check_complete_bet();
DROP TRIGGER complete_win ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_win AFTER INSERT OR UPDATE ON wager_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN((NEW.kind IN ('WIN','REFUND')) AND NEW.status<>'FAILED') EXECUTE FUNCTION check_complete_win();
DROP TRIGGER complete_loss ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_loss AFTER INSERT ON wager_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN((NEW.kind='LOSS') AND NEW.status<>'FAILED') EXECUTE FUNCTION check_complete_loss();
DROP TRIGGER complete_rollback ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_rollback AFTER INSERT OR UPDATE ON wager_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN((NEW.kind='ROLLBACK') AND NEW.status<>'FAILED') EXECUTE FUNCTION check_complete_rollback();

CREATE FUNCTION check_complete_failed() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM wallets WHERE id=NEW.wallet_id AND player_id=NEW.player_id AND currency=NEW.currency
   AND balance_cents=NEW.balance_after_cents AND version=NEW.wallet_version)
   OR EXISTS(SELECT 1 FROM wallet_ledger_entries WHERE transaction_id=NEW.id)
   OR EXISTS(SELECT 1 FROM outbox_events WHERE transaction_id=NEW.id) THEN
  RAISE EXCEPTION 'failed transaction requires unchanged wallet and no financial effects' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER complete_failed AFTER INSERT ON wager_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN(NEW.status='FAILED') EXECUTE FUNCTION check_complete_failed();

CREATE FUNCTION protect_failed_event() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM wager_transactions WHERE id=NEW.transaction_id AND status='FAILED') THEN
  RAISE EXCEPTION 'failed transaction cannot emit business events' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER failed_event_shape BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION protect_failed_event();
COMMIT;
