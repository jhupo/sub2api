ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_read_adjustment JSONB;

COMMENT ON COLUMN usage_logs.cache_read_adjustment IS
    'Request-time cache conversion and incremental charge snapshot; NULL for historical/unadjusted requests';
