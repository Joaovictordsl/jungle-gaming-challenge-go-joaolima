BEGIN;

ALTER TABLE wager_transactions
    ADD COLUMN reference_external_transaction_id TEXT,
    ADD COLUMN reference_transaction_id TEXT,
    ADD COLUMN reference_attempts INTEGER NOT NULL DEFAULT 0 CHECK (reference_attempts >= 0 AND reference_attempts <= 8),
    ADD COLUMN next_reference_attempt_at TIMESTAMPTZ,
    ADD COLUMN reference_deadline TIMESTAMPTZ,
    ADD CONSTRAINT transaction_provider_identity UNIQUE (id, provider_id),
    ADD CONSTRAINT resolved_reference_provider FOREIGN KEY (reference_transaction_id, provider_id)
        REFERENCES wager_transactions (id, provider_id);

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
ALTER TABLE wager_transactions ADD CONSTRAINT win_reference_shape CHECK (
  (kind <> 'WIN' AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL
    AND reference_attempts = 0 AND next_reference_attempt_at IS NULL AND reference_deadline IS NULL)
  OR
  (kind = 'WIN'
   AND (reference_external_transaction_id IS NULL OR (reference_external_transaction_id !~ '^[[:space:]]*$'
     AND octet_length(reference_external_transaction_id) <= 256 AND reference_external_transaction_id <> external_transaction_id))
   AND (reference_transaction_id IS NULL OR reference_external_transaction_id IS NOT NULL)
   AND (reference_deadline IS NULL OR (reference_external_transaction_id IS NOT NULL AND reference_deadline > created_at))
   AND (reference_attempts = 0 OR reference_deadline IS NOT NULL)
   AND ((status = 'PENDING_REFERENCE' AND reference_external_transaction_id IS NOT NULL
         AND reference_transaction_id IS NULL AND next_reference_attempt_at IS NOT NULL AND reference_deadline IS NOT NULL)
     OR (status IN ('PROCESSED', 'REJECTED') AND next_reference_attempt_at IS NULL))
   AND (status <> 'PROCESSED' OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL)
   AND (failure_code IS NULL OR failure_code NOT IN ('REFERENCE_MISMATCH', 'REFERENCE_REJECTED')
     OR (reference_external_transaction_id IS NOT NULL AND reference_transaction_id IS NOT NULL))
   AND (failure_code IS NULL OR failure_code <> 'REFERENCE_NOT_FOUND'
     OR (reference_external_transaction_id IS NOT NULL AND reference_transaction_id IS NULL AND reference_deadline IS NOT NULL))
   AND (failure_code IS NULL OR failure_code <> 'BALANCE_OVERFLOW'
     OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL)
  )
);
CREATE INDEX pending_win_reference ON wager_transactions(next_reference_attempt_at, id)
    WHERE status = 'PENDING_REFERENCE';

ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK
    (event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged', 'WagerTransactionRejected', 'WagerTransactionPendingReference'));

DROP TRIGGER complete_bet ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_bet AFTER INSERT ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind = 'BET') EXECUTE FUNCTION check_complete_bet();

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

DROP TRIGGER opening_immutable ON wager_transactions;
CREATE FUNCTION protect_wager_transaction() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.status <> 'PENDING_REFERENCE' THEN
        RAISE EXCEPTION 'terminal transactions are immutable' USING ERRCODE = '23514';
    END IF;
    IF ROW(NEW.id,NEW.origin,NEW.kind,NEW.wallet_id,NEW.player_id,NEW.currency,NEW.amount_cents,
           NEW.created_at,NEW.provider_id,NEW.external_transaction_id,NEW.idempotency_key,
           NEW.payload_hash,NEW.round_id,NEW.game_id,NEW.reference_external_transaction_id,NEW.reference_deadline)
       IS DISTINCT FROM
       ROW(OLD.id,OLD.origin,OLD.kind,OLD.wallet_id,OLD.player_id,OLD.currency,OLD.amount_cents,
           OLD.created_at,OLD.provider_id,OLD.external_transaction_id,OLD.idempotency_key,
           OLD.payload_hash,OLD.round_id,OLD.game_id,OLD.reference_external_transaction_id,OLD.reference_deadline)
       OR NEW.status NOT IN ('PENDING_REFERENCE','PROCESSED','REJECTED')
       OR NEW.reference_attempts <> OLD.reference_attempts + 1 OR NEW.updated_at < OLD.updated_at
       OR (NEW.status = 'PENDING_REFERENCE' AND NEW.next_reference_attempt_at <= OLD.next_reference_attempt_at) THEN
        RAISE EXCEPTION 'invalid pending reference transition' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER wager_immutable BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION protect_wager_transaction();

CREATE FUNCTION check_complete_win() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE expected_events INTEGER;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM wallets WHERE id = NEW.wallet_id
        AND balance_cents = NEW.balance_after_cents AND version = NEW.wallet_version) THEN
        RAISE EXCEPTION 'win result must match wallet' USING ERRCODE = '23514';
    END IF;
    expected_events := CASE WHEN NEW.reference_deadline IS NULL THEN 0 ELSE 1 END;
    IF expected_events = 1 AND NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WagerTransactionPendingReference') THEN
        RAISE EXCEPTION 'missing reference pending event' USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'PROCESSED' THEN
        IF NEW.reference_external_transaction_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM wager_transactions r WHERE r.id = NEW.reference_transaction_id AND r.kind = 'BET' AND r.status = 'PROCESSED'
              AND r.provider_id = NEW.provider_id AND r.external_transaction_id = NEW.reference_external_transaction_id
              AND r.wallet_id = NEW.wallet_id AND r.player_id = NEW.player_id AND r.currency = NEW.currency
              AND r.round_id = NEW.round_id AND r.game_id = NEW.game_id
        ) THEN RAISE EXCEPTION 'invalid win reference' USING ERRCODE = '23514'; END IF;
        IF NOT EXISTS (SELECT 1 FROM wallet_ledger_entries WHERE transaction_id = NEW.id AND direction = 'CREDIT'
            AND balance_before_cents = NEW.balance_before_cents AND balance_after_cents = NEW.balance_after_cents AND created_at = NEW.updated_at)
          OR NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WagerTransactionProcessed')
          OR NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WalletBalanceChanged') THEN
            RAISE EXCEPTION 'processed win requires credit and both events' USING ERRCODE = '23514';
        END IF;
        expected_events := expected_events + 2;
    ELSE
        IF EXISTS (SELECT 1 FROM wallet_ledger_entries WHERE transaction_id = NEW.id) THEN
            RAISE EXCEPTION 'unprocessed win cannot have ledger' USING ERRCODE = '23514';
        END IF;
        IF NEW.status = 'REJECTED' THEN
            IF NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WagerTransactionRejected') THEN
                RAISE EXCEPTION 'rejected win requires rejection event' USING ERRCODE = '23514';
            END IF;
            expected_events := expected_events + 1;
        END IF;
    END IF;
    IF (SELECT count(*) FROM outbox_events WHERE transaction_id = NEW.id) <> expected_events THEN
        RAISE EXCEPTION 'unexpected win events' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER complete_win AFTER INSERT OR UPDATE ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind = 'WIN') EXECUTE FUNCTION check_complete_win();

COMMIT;
