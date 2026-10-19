BEGIN;
ALTER TABLE wager_transactions DROP CONSTRAINT wager_transactions_amount_cents_check;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_transactions_amount_cents_check
    CHECK (amount_cents >= 0 AND (kind = 'LOSS' OR amount_cents > 0));
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
);
DROP INDEX one_processed_bet_per_version;
CREATE UNIQUE INDEX one_balance_change_per_version ON wager_transactions(wallet_id,wallet_version)
    WHERE origin = 'EXTERNAL' AND status = 'PROCESSED' AND kind IN ('BET','WIN');

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

CREATE FUNCTION check_complete_loss() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM wallets WHERE id = NEW.wallet_id
        AND balance_cents = NEW.balance_after_cents AND version = NEW.wallet_version)
      OR EXISTS (SELECT 1 FROM wallet_ledger_entries WHERE transaction_id = NEW.id)
      OR (SELECT count(*) FROM outbox_events WHERE transaction_id = NEW.id) <> 1
      OR NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WagerTransactionProcessed') THEN
        RAISE EXCEPTION 'loss requires unchanged wallet, no ledger and one processed event' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER complete_loss AFTER INSERT ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind = 'LOSS') EXECUTE FUNCTION check_complete_loss();

CREATE FUNCTION protect_loss_event() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE operation wager_transactions%ROWTYPE;
BEGIN
    SELECT * INTO operation FROM wager_transactions WHERE id = NEW.transaction_id AND kind = 'LOSS';
    IF FOUND AND (NEW.event_type <> 'WagerTransactionProcessed'
        OR NEW.payload->'data'->>'kind' IS DISTINCT FROM 'LOSS'
        OR NEW.payload->'data'->>'status' IS DISTINCT FROM 'PROCESSED'
        OR NEW.payload->'data'->>'transactionId' IS DISTINCT FROM operation.id
        OR NEW.payload->'data'->'walletVersion' IS DISTINCT FROM to_jsonb(operation.wallet_version)
        OR NEW.payload->'data'->'money' IS DISTINCT FROM jsonb_build_object('amount','0.00','currency',operation.currency)) THEN
        RAISE EXCEPTION 'loss can only emit its processed event' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER loss_event_shape BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION protect_loss_event();
COMMIT;
