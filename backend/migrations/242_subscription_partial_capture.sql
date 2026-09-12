ALTER TABLE billing_reservations
    ADD COLUMN IF NOT EXISTS actual_amount DECIMAL(20, 10) NOT NULL DEFAULT 0;

UPDATE billing_reservations
SET actual_amount = captured_amount
WHERE actual_amount = 0
  AND captured_amount > 0;

ALTER TABLE billing_reservations
    DROP CONSTRAINT IF EXISTS billing_reservations_amount_check;
ALTER TABLE billing_reservations
    ADD CONSTRAINT billing_reservations_amount_check CHECK (
        authorized_amount >= 0
        AND captured_amount >= 0
        AND actual_amount >= 0
        AND captured_amount <= authorized_amount
        AND captured_amount <= actual_amount
    );

ALTER TABLE billing_reservations
    DROP CONSTRAINT IF EXISTS billing_reservations_status_check;
ALTER TABLE billing_reservations
    ADD CONSTRAINT billing_reservations_status_check CHECK (
        status IN ('authorized', 'finalizing', 'captured', 'partially_captured', 'released')
    );
