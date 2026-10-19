BEGIN;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE kind = 'WIN') THEN
        RAISE EXCEPTION 'cannot revert WIN migration with win history';
    END IF;
END $$;
DROP TRIGGER complete_win ON wager_transactions;
DROP FUNCTION check_complete_win();
DROP TRIGGER wager_immutable ON wager_transactions;
DROP FUNCTION protect_wager_transaction();
CREATE TRIGGER opening_immutable BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_financial_record_change();
DROP TRIGGER complete_bet ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_bet AFTER INSERT ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.origin = 'EXTERNAL') EXECUTE FUNCTION check_complete_bet();
DROP INDEX pending_win_reference;
ALTER TABLE wager_transactions DROP CONSTRAINT win_reference_shape,
    DROP CONSTRAINT resolved_reference_provider, DROP CONSTRAINT transaction_provider_identity,
    DROP CONSTRAINT transaction_origin_shape,
    DROP COLUMN reference_external_transaction_id, DROP COLUMN reference_transaction_id,
    DROP COLUMN reference_attempts, DROP COLUMN next_reference_attempt_at, DROP COLUMN reference_deadline;
ALTER TABLE wager_transactions
    ADD CONSTRAINT transaction_origin_shape CHECK (
      (origin = 'INTERNAL' AND kind = 'OPENING' AND status = 'PROCESSED'
       AND wallet_version = 1 AND balance_after_cents = amount_cents AND updated_at = created_at
       AND provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
       AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
       AND balance_before_cents IS NULL AND failure_code IS NULL)
      OR
      (origin = 'EXTERNAL' AND kind = 'BET'
       AND provider_id IS NOT NULL AND provider_id !~ '^[[:space:]]*$' AND octet_length(provider_id) <= 256
       AND external_transaction_id IS NOT NULL AND external_transaction_id !~ '^[[:space:]]*$' AND octet_length(external_transaction_id) <= 256
       AND idempotency_key IS NOT NULL AND idempotency_key !~ '^[[:space:]]*$' AND octet_length(idempotency_key) <= 256
       AND payload_hash IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$'
       AND round_id IS NOT NULL AND round_id !~ '^[[:space:]]*$' AND octet_length(round_id) <= 256
       AND game_id IS NOT NULL AND game_id !~ '^[[:space:]]*$' AND octet_length(game_id) <= 256
       AND balance_before_cents IS NOT NULL AND balance_before_cents >= 0 AND balance_after_cents >= 0
       AND updated_at >= created_at
       AND ((status = 'PROCESSED' AND failure_code IS NULL AND wallet_version >= 2
             AND balance_after_cents::NUMERIC = balance_before_cents::NUMERIC - amount_cents::NUMERIC)
         OR (status = 'REJECTED' AND failure_code IS NOT NULL AND failure_code = 'INSUFFICIENT_FUNDS'
             AND wallet_version >= 1 AND balance_after_cents = balance_before_cents AND amount_cents > balance_before_cents)))
    );

CREATE OR REPLACE FUNCTION protect_bet_ledger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE id = NEW.transaction_id AND origin = 'EXTERNAL'
        AND (status <> 'PROCESSED' OR NEW.direction <> 'DEBIT'
             OR NEW.balance_before_cents <> balance_before_cents OR NEW.balance_after_cents <> balance_after_cents
             OR NEW.created_at <> updated_at)) THEN
        RAISE EXCEPTION 'ledger must match processed bet' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK
    (event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged', 'WagerTransactionRejected'));
COMMIT;
