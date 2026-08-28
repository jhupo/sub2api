-- Remove the fixed recharge-center menu item used by the retired custom UI.
-- Other custom menu items, including migrated_purchase_subscription, are preserved.
DO $$
DECLARE
    v_raw      text;
    v_items    jsonb;
    v_filtered jsonb;
BEGIN
    SELECT value
    INTO v_raw
    FROM settings
    WHERE key = 'custom_menu_items'
    FOR UPDATE;

    IF NOT FOUND OR COALESCE(BTRIM(v_raw), '') = '' THEN
        RETURN;
    END IF;

    BEGIN
        v_items := v_raw::jsonb;
    EXCEPTION
        WHEN invalid_text_representation THEN
            RETURN;
    END;

    IF jsonb_typeof(v_items) IS DISTINCT FROM 'array' THEN
        RETURN;
    END IF;

    SELECT COALESCE(jsonb_agg(menu_item ORDER BY ordinal), '[]'::jsonb)
    INTO v_filtered
    FROM jsonb_array_elements(v_items) WITH ORDINALITY AS menu(menu_item, ordinal)
    WHERE menu_item ->> 'id' IS DISTINCT FROM '322273f5aaa4d036';

    IF v_filtered IS DISTINCT FROM v_items THEN
        UPDATE settings
        SET value = v_filtered::text,
            updated_at = NOW()
        WHERE key = 'custom_menu_items';
    END IF;
END $$;
