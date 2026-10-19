BEGIN;
CREATE INDEX transaction_wallet_financial_order ON wager_transactions(wallet_id,wallet_version,id)
 WHERE status='PROCESSED' AND kind<>'LOSS';
COMMIT;
