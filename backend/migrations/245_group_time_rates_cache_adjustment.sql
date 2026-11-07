ALTER TABLE groups
    ADD COLUMN night_rate_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN night_start VARCHAR(5) NOT NULL DEFAULT '',
    ADD COLUMN night_end VARCHAR(5) NOT NULL DEFAULT '',
    ADD COLUMN night_rate_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1,
    ADD COLUMN cache_read_adjustment_mode VARCHAR(16) NOT NULL DEFAULT 'inherit',
    ADD COLUMN cache_read_adjustment_percent INTEGER NOT NULL DEFAULT 10,
    ADD CONSTRAINT groups_cache_adjustment_mode_check CHECK (cache_read_adjustment_mode IN ('inherit', 'custom', 'disabled')),
    ADD CONSTRAINT groups_cache_adjustment_percent_check CHECK (cache_read_adjustment_percent BETWEEN 0 AND 100),
    ADD CONSTRAINT groups_night_multiplier_check CHECK (night_rate_multiplier >= 0);
