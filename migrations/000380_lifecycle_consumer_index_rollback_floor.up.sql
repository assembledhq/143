-- The paired down migration checks the retained lifecycle floor before any
-- preceding concurrent index migration can remove its supporting indexes.
SELECT 1;
