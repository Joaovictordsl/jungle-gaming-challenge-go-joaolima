BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM inbox_messages) THEN
  RAISE EXCEPTION 'cannot revert inbox migration with audit history';
 END IF;
END $$;
DROP TABLE inbox_messages;
DROP FUNCTION protect_inbox();
DROP FUNCTION check_complete_inbox();
COMMIT;
