-- Cannot reliably restore the prior 'starting' / 'provisioning' / 'ready' /
-- 'healthy' values: by definition the rows we touched belonged to terminal
-- parents, so there is no signal to recover from. Leave the rows as the
-- migration left them.
SELECT 1;
