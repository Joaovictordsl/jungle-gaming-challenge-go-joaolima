BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM outbox_events WHERE attempts>0 OR locked_by IS NOT NULL OR published_at IS NOT NULL) THEN
  RAISE EXCEPTION 'cannot revert outbox publication with delivery history';
 END IF;
END; $$;
CREATE OR REPLACE FUNCTION protect_outbox_snapshot() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'outbox events cannot be removed' USING ERRCODE='23514'; END IF;
 IF ROW(NEW.id,NEW.transaction_id,NEW.aggregate_id,NEW.event_type,NEW.payload,NEW.occurred_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.transaction_id,OLD.aggregate_id,OLD.event_type,OLD.payload,OLD.occurred_at) THEN
  RAISE EXCEPTION 'outbox snapshot is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;
DROP INDEX outbox_aggregate_pending;
ALTER TABLE outbox_events DROP COLUMN publication_sequence;
COMMIT;
