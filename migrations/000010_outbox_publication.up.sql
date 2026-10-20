BEGIN;
CREATE SEQUENCE outbox_publication_sequence;
ALTER TABLE outbox_events ADD COLUMN publication_sequence BIGINT;
WITH ordered AS (
 SELECT id,row_number() OVER (ORDER BY occurred_at,id) AS position FROM outbox_events
)
UPDATE outbox_events e SET publication_sequence=o.position FROM ordered o WHERE e.id=o.id;
SELECT setval('outbox_publication_sequence',COALESCE(max(publication_sequence),1),max(publication_sequence) IS NOT NULL) FROM outbox_events;
ALTER TABLE outbox_events ALTER COLUMN publication_sequence SET DEFAULT nextval('outbox_publication_sequence');
ALTER TABLE outbox_events ALTER COLUMN publication_sequence SET NOT NULL;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_publication_sequence_positive CHECK(publication_sequence>0);
ALTER TABLE outbox_events ADD CONSTRAINT outbox_publication_sequence_unique UNIQUE(publication_sequence);
ALTER SEQUENCE outbox_publication_sequence OWNED BY outbox_events.publication_sequence;
CREATE INDEX outbox_aggregate_pending ON outbox_events(aggregate_id,publication_sequence) WHERE published_at IS NULL;
CREATE OR REPLACE FUNCTION protect_outbox_snapshot() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'outbox events cannot be removed' USING ERRCODE='23514'; END IF;
 IF ROW(NEW.id,NEW.transaction_id,NEW.aggregate_id,NEW.event_type,NEW.payload,NEW.occurred_at,NEW.publication_sequence)
 IS DISTINCT FROM ROW(OLD.id,OLD.transaction_id,OLD.aggregate_id,OLD.event_type,OLD.payload,OLD.occurred_at,OLD.publication_sequence) THEN
  RAISE EXCEPTION 'outbox snapshot is immutable' USING ERRCODE='23514';
 END IF;
 IF OLD.published_at IS NOT NULL AND ROW(NEW.attempts,NEW.next_attempt_at,NEW.published_at,NEW.locked_by,NEW.locked_until)
 IS DISTINCT FROM ROW(OLD.attempts,OLD.next_attempt_at,OLD.published_at,OLD.locked_by,OLD.locked_until) THEN
  RAISE EXCEPTION 'published outbox metadata is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;
COMMIT;
