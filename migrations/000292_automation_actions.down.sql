-- Action identities must survive application rollback to prevent duplicate posts.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM automation_actions) THEN
        RAISE EXCEPTION 'automation action receipts exist; roll back application code without dropping migration 000292';
    END IF;
END $$;
DROP TABLE automation_actions;
