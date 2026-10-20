BEGIN;
CREATE TABLE inbox_messages (
 consumer_name TEXT NOT NULL CHECK(octet_length(consumer_name) BETWEEN 1 AND 256 AND consumer_name !~ '^[[:space:]]*$'),
 message_id TEXT NOT NULL CHECK(octet_length(message_id) BETWEEN 1 AND 256 AND message_id !~ '^[[:space:]]*$'),
 payload_hash TEXT NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
 provider_id TEXT NOT NULL,
 external_transaction_id TEXT NOT NULL,
 wallet_id TEXT NOT NULL,
 occurred_at TIMESTAMPTZ NOT NULL,
 received_at TIMESTAMPTZ NOT NULL,
 completed_at TIMESTAMPTZ,
 status TEXT NOT NULL CHECK(status IN ('PENDING','COMPLETED','FAILED')),
 transaction_id TEXT,
 result_status TEXT,
 result_failure_code TEXT,
 balance_cents BIGINT,
 currency TEXT,
 wallet_version BIGINT,
 financial_replay BOOLEAN,
 failure_code TEXT,
 PRIMARY KEY(consumer_name,message_id),
 FOREIGN KEY(transaction_id,wallet_id) REFERENCES wager_transactions(id,wallet_id),
 FOREIGN KEY(transaction_id,provider_id) REFERENCES wager_transactions(id,provider_id),
 CONSTRAINT inbox_shape CHECK (
  (status='PENDING' AND completed_at IS NULL AND transaction_id IS NULL AND result_status IS NULL
   AND result_failure_code IS NULL AND balance_cents IS NULL AND currency IS NULL AND wallet_version IS NULL AND financial_replay IS NULL AND failure_code IS NULL)
  OR (status='COMPLETED' AND completed_at IS NOT NULL AND completed_at>=received_at AND transaction_id IS NOT NULL
   AND result_status IS NOT NULL AND result_status IN ('PROCESSED','REJECTED','PENDING_REFERENCE')
   AND ((result_status='REJECTED' AND result_failure_code IS NOT NULL) OR (result_status<>'REJECTED' AND result_failure_code IS NULL))
   AND balance_cents IS NOT NULL AND balance_cents>=0 AND currency IS NOT NULL AND currency IN ('BRL','USD')
   AND wallet_version IS NOT NULL AND wallet_version>=1 AND financial_replay IS NOT NULL AND failure_code IS NULL)
  OR (status='FAILED' AND completed_at IS NOT NULL AND completed_at>=received_at AND failure_code IS NOT NULL
   AND transaction_id IS NULL AND result_status IS NULL AND result_failure_code IS NULL AND balance_cents IS NULL AND currency IS NULL AND wallet_version IS NULL AND financial_replay IS NULL)
 )
);
CREATE FUNCTION protect_inbox() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status<>'PENDING' THEN
  RAISE EXCEPTION 'completed inbox is immutable' USING ERRCODE='23514';
 END IF;
 IF ROW(NEW.consumer_name,NEW.message_id,NEW.payload_hash,NEW.provider_id,NEW.external_transaction_id,NEW.wallet_id,NEW.occurred_at,NEW.received_at)
  IS DISTINCT FROM ROW(OLD.consumer_name,OLD.message_id,OLD.payload_hash,OLD.provider_id,OLD.external_transaction_id,OLD.wallet_id,OLD.occurred_at,OLD.received_at)
  OR NEW.status NOT IN ('COMPLETED','FAILED') THEN
  RAISE EXCEPTION 'invalid inbox transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER inbox_immutable BEFORE UPDATE OR DELETE ON inbox_messages FOR EACH ROW EXECUTE FUNCTION protect_inbox();
CREATE TRIGGER inbox_no_truncate BEFORE TRUNCATE ON inbox_messages FOR EACH STATEMENT EXECUTE FUNCTION forbid_financial_record_change();
CREATE FUNCTION check_complete_inbox() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM inbox_messages WHERE consumer_name=NEW.consumer_name AND message_id=NEW.message_id AND status='PENDING') THEN
  RAISE EXCEPTION 'inbox cannot commit unfinished work' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER inbox_complete AFTER INSERT OR UPDATE ON inbox_messages DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_complete_inbox();
COMMIT;
