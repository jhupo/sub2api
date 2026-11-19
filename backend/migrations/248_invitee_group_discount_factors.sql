ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS invitee_group_discount_factors JSONB NOT NULL DEFAULT '{}'::jsonb;

DROP INDEX IF EXISTS idx_user_affiliates_admin_settings;
CREATE INDEX idx_user_affiliates_admin_settings
    ON user_affiliates (updated_at)
    WHERE aff_code_custom = true
       OR aff_rebate_rate_percent IS NOT NULL
       OR invitee_group_discount_factors <> '{}'::jsonb;

COMMENT ON COLUMN user_affiliates.invitee_group_discount_factors IS
    '按分组设置的被邀请人折扣系数，键为 group_id，值为 0 到 1 的倍率';
