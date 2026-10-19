BEGIN;

ALTER TABLE wager_transactions
    DROP CONSTRAINT wager_transactions_origin_check,
    DROP CONSTRAINT wager_transactions_kind_check,
    DROP CONSTRAINT wager_transactions_status_check,
    DROP CONSTRAINT wager_transactions_wallet_version_check,
    DROP CONSTRAINT wager_transactions_wallet_id_key,
    DROP CONSTRAINT opening_result,
    DROP CONSTRAINT opening_time,
    ADD COLUMN provider_id TEXT,
    ADD COLUMN external_transaction_id TEXT,
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN payload_hash TEXT,
    ADD COLUMN round_id TEXT,
    ADD COLUMN game_id TEXT,
    ADD COLUMN balance_before_cents BIGINT,
    ADD COLUMN failure_code TEXT;

CREATE UNIQUE INDEX one_internal_opening ON wager_transactions(wallet_id) WHERE origin = 'INTERNAL';
CREATE UNIQUE INDEX one_processed_bet_per_version ON wager_transactions(wallet_id, wallet_version)
    WHERE origin = 'EXTERNAL' AND status = 'PROCESSED';
ALTER TABLE wager_transactions
    ADD CONSTRAINT bet_provider_key_unique UNIQUE(provider_id, idempotency_key),
    ADD CONSTRAINT bet_provider_external_unique UNIQUE(provider_id, external_transaction_id),
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

ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check
    CHECK (event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged', 'WagerTransactionRejected'));

DROP TRIGGER complete_opening ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_opening AFTER INSERT ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.origin = 'INTERNAL')
    EXECUTE FUNCTION check_complete_opening();

CREATE FUNCTION check_complete_bet() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM wallets WHERE id = NEW.wallet_id
        AND balance_cents = NEW.balance_after_cents AND version = NEW.wallet_version) THEN
        RAISE EXCEPTION 'bet result must match wallet' USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'PROCESSED' THEN
        IF NOT EXISTS (SELECT 1 FROM wallet_ledger_entries WHERE transaction_id = NEW.id
            AND direction = 'DEBIT' AND balance_before_cents = NEW.balance_before_cents
            AND balance_after_cents = NEW.balance_after_cents AND created_at = NEW.updated_at)
            OR (SELECT count(*) FROM outbox_events WHERE transaction_id = NEW.id) <> 2
            OR NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WagerTransactionProcessed')
            OR NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WalletBalanceChanged') THEN
            RAISE EXCEPTION 'processed bet requires debit and both events' USING ERRCODE = '23514';
        END IF;
    ELSE
        IF EXISTS (SELECT 1 FROM wallet_ledger_entries WHERE transaction_id = NEW.id)
            OR (SELECT count(*) FROM outbox_events WHERE transaction_id = NEW.id) <> 1
            OR NOT EXISTS (SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id AND event_type = 'WagerTransactionRejected') THEN
            RAISE EXCEPTION 'rejected bet requires one rejection event and no ledger' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER complete_bet AFTER INSERT ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.origin = 'EXTERNAL')
    EXECUTE FUNCTION check_complete_bet();

CREATE FUNCTION protect_bet_ledger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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
CREATE TRIGGER bet_ledger_shape BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION protect_bet_ledger();

CREATE FUNCTION check_wallet_movement() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.id, NEW.player_id, NEW.currency, NEW.created_at) IS DISTINCT FROM
       ROW(OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallet identity is immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.balance_cents = OLD.balance_cents THEN
        IF NEW.version <> OLD.version OR NEW.updated_at <> OLD.updated_at THEN
            RAISE EXCEPTION 'wallet version requires balance change' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.version::NUMERIC <> OLD.version::NUMERIC + 1 OR NEW.updated_at < OLD.updated_at OR NOT EXISTS (
        SELECT 1 FROM wager_transactions t JOIN wallet_ledger_entries l ON l.transaction_id = t.id
        WHERE t.wallet_id = NEW.id AND t.origin = 'EXTERNAL' AND t.status = 'PROCESSED'
          AND t.wallet_version = NEW.version AND t.balance_before_cents = OLD.balance_cents
          AND t.balance_after_cents = NEW.balance_cents AND t.updated_at = NEW.updated_at
          AND l.balance_before_cents = OLD.balance_cents AND l.balance_after_cents = NEW.balance_cents
    ) THEN
        RAISE EXCEPTION 'wallet movement requires its transaction and ledger' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER wallet_movement AFTER UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_wallet_movement();

COMMIT;
