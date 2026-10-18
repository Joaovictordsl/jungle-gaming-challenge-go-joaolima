BEGIN;
DROP TABLE outbox_events;
DROP TABLE wallet_ledger_entries;
DROP TABLE wager_transactions;
DROP FUNCTION check_complete_opening();
DROP FUNCTION protect_outbox_snapshot();
DROP FUNCTION forbid_financial_record_change();
ALTER TABLE wallets DROP CONSTRAINT wallets_identity_currency_unique;
COMMIT;
