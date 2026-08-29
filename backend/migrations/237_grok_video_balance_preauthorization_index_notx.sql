CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_balance_settlements_async_task_api_key
    ON billing_balance_settlements (async_task_id, api_key_id)
    WHERE async_task_id IS NOT NULL;
