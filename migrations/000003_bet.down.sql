BEGIN;
-- Não apague histórico financeiro para voltar a uma versão sem BET.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE origin = 'EXTERNAL') THEN
        RAISE EXCEPTION 'cannot revert BET migration with external transactions';
    END IF;
END $$;
DROP TRIGGER wallet_movement ON wallets;
DROP FUNCTION check_wallet_movement();
DROP TRIGGER bet_ledger_shape ON wallet_ledger_entries;
DROP FUNCTION protect_bet_ledger();
DROP TRIGGER complete_bet ON wager_transactions;
DROP FUNCTION check_complete_bet();
DROP TRIGGER complete_opening ON wager_transactions;
CREATE CONSTRAINT TRIGGER complete_opening AFTER INSERT ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_complete_opening();
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check
    CHECK (event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged'));
DROP INDEX one_internal_opening;
DROP INDEX one_processed_bet_per_version;
ALTER TABLE wager_transactions
    DROP CONSTRAINT transaction_origin_shape,
    DROP CONSTRAINT bet_provider_key_unique,
    DROP CONSTRAINT bet_provider_external_unique,
    DROP COLUMN provider_id, DROP COLUMN external_transaction_id, DROP COLUMN idempotency_key,
    DROP COLUMN payload_hash, DROP COLUMN round_id, DROP COLUMN game_id,
    DROP COLUMN balance_before_cents, DROP COLUMN failure_code,
    ADD CONSTRAINT wager_transactions_origin_check CHECK (origin = 'INTERNAL'),
    ADD CONSTRAINT wager_transactions_kind_check CHECK (kind = 'OPENING'),
    ADD CONSTRAINT wager_transactions_status_check CHECK (status = 'PROCESSED'),
    ADD CONSTRAINT wager_transactions_wallet_version_check CHECK (wallet_version = 1),
    ADD CONSTRAINT wager_transactions_wallet_id_key UNIQUE(wallet_id),
    ADD CONSTRAINT opening_result CHECK (balance_after_cents = amount_cents),
    ADD CONSTRAINT opening_time CHECK (updated_at = created_at);
COMMIT;
