ALTER TABLE api_tokens
ADD COLUMN allowed_ip_cidrs text[] NOT NULL DEFAULT '{}';
