BEGIN;

ALTER TABLE wallets ADD CONSTRAINT wallets_identity_currency_unique
    UNIQUE (id, player_id, currency);

-- Esta etapa persiste apenas transações internas OPENING. Operações externas
-- serão acrescentadas por outra migration, com seus metadados obrigatórios.
CREATE TABLE wager_transactions (
    id TEXT PRIMARY KEY CHECK (id !~ '^[[:space:]]*$'),
    origin TEXT NOT NULL CHECK (origin = 'INTERNAL'),
    kind TEXT NOT NULL CHECK (kind = 'OPENING'),
    wallet_id TEXT NOT NULL UNIQUE,
    player_id TEXT NOT NULL,
    currency TEXT NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    status TEXT NOT NULL CHECK (status = 'PROCESSED'),
    balance_after_cents BIGINT NOT NULL,
    wallet_version BIGINT NOT NULL CHECK (wallet_version = 1),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT opening_result CHECK (balance_after_cents = amount_cents),
    CONSTRAINT opening_time CHECK (updated_at = created_at),
    CONSTRAINT opening_wallet_identity FOREIGN KEY (wallet_id, player_id, currency)
        REFERENCES wallets (id, player_id, currency),
    CONSTRAINT transaction_wallet_unique UNIQUE (id, wallet_id),
    CONSTRAINT transaction_amount_unique UNIQUE (id, wallet_id, currency, amount_cents)
);

CREATE TABLE wallet_ledger_entries (
    id TEXT PRIMARY KEY CHECK (id !~ '^[[:space:]]*$'),
    wallet_id TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('CREDIT', 'DEBIT')),
    currency TEXT NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    balance_before_cents BIGINT NOT NULL CHECK (balance_before_cents >= 0),
    balance_after_cents BIGINT NOT NULL CHECK (balance_after_cents >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_wallet_transaction_unique UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_transaction_amount FOREIGN KEY (transaction_id, wallet_id, currency, amount_cents)
        REFERENCES wager_transactions (id, wallet_id, currency, amount_cents),
    -- NUMERIC evita overflow na própria verificação; os valores continuam BIGINT.
    CONSTRAINT ledger_balance_equation CHECK (
        (direction = 'CREDIT' AND balance_after_cents::NUMERIC = balance_before_cents::NUMERIC + amount_cents::NUMERIC)
        OR (direction = 'DEBIT' AND balance_after_cents::NUMERIC = balance_before_cents::NUMERIC - amount_cents::NUMERIC)
    )
);

CREATE INDEX wallet_ledger_cursor ON wallet_ledger_entries (wallet_id, created_at, id);

CREATE TABLE outbox_events (
    id TEXT PRIMARY KEY CHECK (id !~ '^[[:space:]]*$'),
    transaction_id TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged')),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    locked_by TEXT,
    locked_until TIMESTAMPTZ,
    CONSTRAINT outbox_transaction FOREIGN KEY (transaction_id, aggregate_id)
        REFERENCES wager_transactions (id, wallet_id),
    CONSTRAINT outbox_transaction_type_unique UNIQUE (transaction_id, event_type),
    CONSTRAINT outbox_lock_pair CHECK ((locked_by IS NULL) = (locked_until IS NULL)),
    CONSTRAINT outbox_envelope CHECK (
        payload ?& ARRAY['eventId', 'eventType', 'aggregateId', 'correlationId', 'occurredAt', 'version', 'data']
        AND (payload->>'eventId' = id) IS TRUE
        AND (payload->>'eventType' = event_type) IS TRUE
        AND (payload->>'aggregateId' = aggregate_id) IS TRUE
        AND (payload->>'correlationId' = transaction_id) IS TRUE
        AND (payload->'version' = '1'::jsonb) IS TRUE
        AND (jsonb_typeof(payload->'data') = 'object') IS TRUE
    )
);

CREATE INDEX outbox_pending ON outbox_events (next_attempt_at, id) WHERE published_at IS NULL;

CREATE FUNCTION forbid_financial_record_change() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'financial records are immutable' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER ledger_immutable BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_financial_record_change();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_financial_record_change();
CREATE TRIGGER opening_immutable BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_financial_record_change();
CREATE TRIGGER opening_no_truncate BEFORE TRUNCATE ON wager_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_financial_record_change();

CREATE FUNCTION protect_outbox_snapshot() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'UPDATE' THEN
        RAISE EXCEPTION 'outbox events cannot be removed' USING ERRCODE = '23514';
    END IF;
    IF ROW(NEW.id, NEW.transaction_id, NEW.aggregate_id, NEW.event_type, NEW.payload, NEW.occurred_at)
        IS DISTINCT FROM ROW(OLD.id, OLD.transaction_id, OLD.aggregate_id, OLD.event_type, OLD.payload, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox snapshot is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER outbox_snapshot_immutable BEFORE UPDATE OR DELETE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION protect_outbox_snapshot();
CREATE TRIGGER outbox_no_truncate BEFORE TRUNCATE ON outbox_events
    FOR EACH STATEMENT EXECUTE FUNCTION protect_outbox_snapshot();

-- A verificação ocorre no commit, depois que ledger e outbox foram inseridos.
CREATE FUNCTION check_complete_opening() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM wallets
        WHERE id = NEW.wallet_id AND player_id = NEW.player_id AND currency = NEW.currency
          AND balance_cents = NEW.amount_cents AND version = 1
          AND created_at = NEW.created_at AND updated_at = NEW.created_at
    ) OR NOT EXISTS (
        SELECT 1 FROM wallet_ledger_entries
        WHERE transaction_id = NEW.id AND wallet_id = NEW.wallet_id
          AND direction = 'CREDIT' AND balance_before_cents = 0
          AND balance_after_cents = NEW.amount_cents AND created_at = NEW.created_at
    ) OR (SELECT count(*) FROM outbox_events WHERE transaction_id = NEW.id) <> 2 THEN
        RAISE EXCEPTION 'opening requires its credit and both outbox events' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER complete_opening AFTER INSERT ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_complete_opening();

COMMIT;
