-- Bind accepted asynchronous Grok video tasks to their authorized balance hold.
-- Redis keeps a cache copy of the pending billing snapshot; this is the durable
-- recovery source when cache data is evicted or unavailable.
ALTER TABLE billing_balance_settlements
    ADD COLUMN IF NOT EXISTS async_task_id TEXT,
    ADD COLUMN IF NOT EXISTS async_metadata JSONB;
