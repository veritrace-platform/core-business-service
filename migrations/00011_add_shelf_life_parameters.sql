-- Shelf-life parameters (data-model.md §3.2–§3.3; cold-chain-monitoring.md §8). A product's Q10 says how many times
-- faster its goods age for every 10 °C above its maximum temperature. Lots copy it at commissioning, and shipments
-- copy it from the lot together with the lot's production date, as they copy the temperature bounds.

-- +goose Up
ALTER TABLE core.products
    ADD COLUMN shelf_life_q10 numeric(4, 2) NOT NULL DEFAULT 3.00,
    ADD CONSTRAINT products_shelf_life_q10_check CHECK (shelf_life_q10 BETWEEN 1 AND 10);

-- Every product has the default Q10 at this point, so existing lots and shipments copy it by default. New rows
-- always copy it explicitly.
ALTER TABLE core.lots
    ADD COLUMN shelf_life_q10 numeric(4, 2) NOT NULL DEFAULT 3.00,
    ADD CONSTRAINT lots_shelf_life_q10_check CHECK (shelf_life_q10 BETWEEN 1 AND 10);
ALTER TABLE core.lots ALTER COLUMN shelf_life_q10 DROP DEFAULT;

ALTER TABLE core.shipments
    ADD COLUMN production_date date,
    ADD COLUMN shelf_life_q10 numeric(4, 2) NOT NULL DEFAULT 3.00,
    ADD CONSTRAINT shipments_shelf_life_q10_check CHECK (shelf_life_q10 BETWEEN 1 AND 10);
ALTER TABLE core.shipments ALTER COLUMN shelf_life_q10 DROP DEFAULT;

-- The production date of existing shipments comes from their lot. Filling it in is not a change of the shipment,
-- so updated_at keeps its value.
ALTER TABLE core.shipments DISABLE TRIGGER set_updated_at;
UPDATE core.shipments s
SET production_date = l.production_date
FROM core.lots l
WHERE l.id = s.lot_id;
ALTER TABLE core.shipments ENABLE TRIGGER set_updated_at;

ALTER TABLE core.shipments
    ALTER COLUMN production_date SET NOT NULL,
    ADD CONSTRAINT shipments_expiration_date_check CHECK (expiration_date >= production_date);

-- +goose Down
ALTER TABLE core.shipments
    DROP CONSTRAINT shipments_expiration_date_check,
    DROP COLUMN shelf_life_q10,
    DROP COLUMN production_date;
ALTER TABLE core.lots DROP COLUMN shelf_life_q10;
ALTER TABLE core.products DROP COLUMN shelf_life_q10;
