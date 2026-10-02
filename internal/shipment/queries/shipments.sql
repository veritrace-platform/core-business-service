-- name: LockLotForShipment :exec
-- Shipment creation takes the lot's lock in shared mode and a recall takes it exclusively, so a recall waits for
-- shipments being created from the lot, and blocks new ones until it commits.
SELECT pg_advisory_xact_lock_shared(47154002, hashtext(sqlc.arg(lot_id)::uuid::text));

-- name: GetLot :one
SELECT
    id,
    gtin,
    product_name,
    min_temp_celsius,
    max_temp_celsius,
    lot_number,
    expiration_date,
    status
FROM core.lots
WHERE id = sqlc.arg(id);

-- name: GetBalance :one
SELECT quantity_on_hand
FROM core.inventory_balances
WHERE location_id = sqlc.arg(location_id)
  AND lot_id = sqlc.arg(lot_id);

-- name: LockLocation :one
SELECT
    id,
    gln,
    name,
    latitude,
    longitude,
    geo_fence_radius_meters,
    is_active
FROM core.locations
WHERE id = sqlc.arg(id)
FOR SHARE;

-- name: LookupLocation :one
SELECT
    location_id,
    gln,
    name,
    latitude,
    longitude,
    geo_fence_radius_meters,
    tenant_id,
    tenant_code,
    tenant_legal_name
FROM core.lookup_location_by_gln(sqlc.arg(gln));

-- name: LookupTenant :one
SELECT
    tenant_id,
    code,
    legal_name
FROM core.lookup_tenant_by_code(sqlc.arg(code));

-- name: GetOwnTenant :one
SELECT
    id,
    code,
    legal_name
FROM core.tenants
WHERE id = sqlc.arg(id);

-- name: TakeSSCCSerial :one
UPDATE core.tenants
SET sscc_next_serial = sscc_next_serial + 1
WHERE id = sqlc.arg(id)
RETURNING
    (sscc_next_serial - 1)::bigint AS serial,
    sscc_extension_digit,
    gs1_company_prefix;

-- name: GetUser :one
SELECT
    role,
    is_active
FROM core.users
WHERE id = sqlc.arg(id);

-- name: InsertShipment :one
INSERT INTO core.shipments (
    owner_tenant_id,
    sscc,
    lot_id,
    quantity,
    gtin,
    product_name,
    lot_number,
    expiration_date,
    min_temp_celsius,
    max_temp_celsius,
    origin_location_id,
    origin_gln,
    origin_name,
    origin_latitude,
    origin_longitude,
    origin_geo_fence_radius_meters,
    destination_location_id,
    destination_gln,
    destination_name,
    destination_latitude,
    destination_longitude,
    destination_geo_fence_radius_meters,
    consignee_tenant_id,
    carrier_tenant_id,
    assigned_driver_id,
    created_by,
    created_at,
    updated_at
)
VALUES (
    sqlc.arg(owner_tenant_id),
    sqlc.arg(sscc),
    sqlc.arg(lot_id),
    sqlc.arg(quantity),
    sqlc.arg(gtin),
    sqlc.arg(product_name),
    sqlc.arg(lot_number),
    sqlc.arg(expiration_date),
    sqlc.arg(min_temp_celsius),
    sqlc.arg(max_temp_celsius),
    sqlc.arg(origin_location_id),
    sqlc.arg(origin_gln),
    sqlc.arg(origin_name),
    sqlc.arg(origin_latitude),
    sqlc.arg(origin_longitude),
    sqlc.arg(origin_geo_fence_radius_meters),
    sqlc.arg(destination_location_id),
    sqlc.arg(destination_gln),
    sqlc.arg(destination_name),
    sqlc.arg(destination_latitude),
    sqlc.arg(destination_longitude),
    sqlc.arg(destination_geo_fence_radius_meters),
    sqlc.arg(consignee_tenant_id),
    sqlc.arg(carrier_tenant_id),
    sqlc.narg(assigned_driver_id),
    sqlc.arg(created_by),
    sqlc.arg(created_at),
    sqlc.arg(created_at)
)
RETURNING id;

-- name: InsertParticipant :exec
INSERT INTO core.shipment_participants (shipment_id, tenant_id, role, tenant_code, tenant_legal_name, created_at)
VALUES (
    sqlc.arg(shipment_id),
    sqlc.arg(tenant_id),
    sqlc.arg(role),
    sqlc.arg(tenant_code),
    sqlc.arg(tenant_legal_name),
    sqlc.arg(created_at)
);

-- name: DeleteParticipant :exec
DELETE FROM core.shipment_participants
WHERE shipment_id = sqlc.arg(shipment_id)
  AND tenant_id = sqlc.arg(tenant_id)
  AND role = sqlc.arg(role);

-- name: GetShipment :one
SELECT
    id,
    owner_tenant_id,
    sscc,
    lot_id,
    quantity,
    gtin,
    product_name,
    lot_number,
    expiration_date,
    min_temp_celsius,
    max_temp_celsius,
    origin_location_id,
    origin_gln,
    origin_name,
    origin_latitude,
    origin_longitude,
    origin_geo_fence_radius_meters,
    destination_location_id,
    destination_gln,
    destination_name,
    destination_latitude,
    destination_longitude,
    destination_geo_fence_radius_meters,
    consignee_tenant_id,
    carrier_tenant_id,
    assigned_driver_id,
    status,
    picked_up_at,
    delivered_at,
    cancelled_at,
    recalled_at,
    created_at,
    updated_at
FROM core.shipments
WHERE id = sqlc.arg(id);

-- name: LockShipment :one
SELECT
    id,
    owner_tenant_id,
    sscc,
    lot_id,
    quantity,
    gtin,
    product_name,
    lot_number,
    expiration_date,
    min_temp_celsius,
    max_temp_celsius,
    origin_location_id,
    origin_gln,
    origin_name,
    origin_latitude,
    origin_longitude,
    origin_geo_fence_radius_meters,
    destination_location_id,
    destination_gln,
    destination_name,
    destination_latitude,
    destination_longitude,
    destination_geo_fence_radius_meters,
    consignee_tenant_id,
    carrier_tenant_id,
    assigned_driver_id,
    status,
    picked_up_at,
    delivered_at,
    cancelled_at,
    recalled_at,
    created_at,
    updated_at
FROM core.shipments
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: ListShipments :many
SELECT
    s.id,
    s.owner_tenant_id,
    s.sscc,
    s.lot_id,
    s.quantity,
    s.gtin,
    s.product_name,
    s.lot_number,
    s.expiration_date,
    s.min_temp_celsius,
    s.max_temp_celsius,
    s.origin_location_id,
    s.origin_gln,
    s.origin_name,
    s.origin_latitude,
    s.origin_longitude,
    s.origin_geo_fence_radius_meters,
    s.destination_location_id,
    s.destination_gln,
    s.destination_name,
    s.destination_latitude,
    s.destination_longitude,
    s.destination_geo_fence_radius_meters,
    s.consignee_tenant_id,
    s.carrier_tenant_id,
    s.assigned_driver_id,
    s.status,
    s.picked_up_at,
    s.delivered_at,
    s.cancelled_at,
    s.recalled_at,
    s.created_at,
    s.updated_at
FROM core.shipments s
WHERE s.id IN (
        SELECT p.shipment_id
        FROM core.shipment_participants p
        WHERE p.tenant_id = (SELECT core.current_tenant_id())
          AND (sqlc.narg(party)::text IS NULL OR p.role = sqlc.narg(party))
    )
  AND (sqlc.narg(status)::text IS NULL OR s.status = sqlc.narg(status))
  AND (sqlc.narg(sscc)::text IS NULL OR s.sscc = sqlc.narg(sscc))
  AND (sqlc.narg(lot_id)::uuid IS NULL OR s.lot_id = sqlc.narg(lot_id))
  AND (sqlc.narg(driver_id)::uuid IS NULL OR s.assigned_driver_id = sqlc.narg(driver_id))
  AND (sqlc.narg(after)::uuid IS NULL OR s.id < sqlc.narg(after))
ORDER BY s.id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountShipments :one
SELECT
    count(*) FILTER (WHERE s.status = 'CREATED') AS created,
    count(*) FILTER (WHERE s.status = 'IN_TRANSIT') AS in_transit,
    count(*) FILTER (WHERE s.status = 'DELIVERED') AS delivered,
    count(*) FILTER (WHERE s.status = 'CANCELLED') AS cancelled,
    count(*) FILTER (WHERE s.status = 'RECALLED') AS recalled
FROM core.shipments s
WHERE s.id IN (
        SELECT p.shipment_id
        FROM core.shipment_participants p
        WHERE p.tenant_id = (SELECT core.current_tenant_id())
    )
  AND (sqlc.narg(driver_id)::uuid IS NULL OR s.assigned_driver_id = sqlc.narg(driver_id));

-- name: ListParticipants :many
SELECT
    shipment_id,
    tenant_id,
    role,
    tenant_code,
    tenant_legal_name
FROM core.shipment_participants
WHERE shipment_id = ANY (sqlc.arg(shipment_ids)::uuid[])
ORDER BY shipment_id, created_at, role;

-- name: SetCarrier :exec
UPDATE core.shipments
SET carrier_tenant_id = sqlc.arg(carrier_tenant_id),
    assigned_driver_id = NULL
WHERE id = sqlc.arg(id);

-- name: SetDriver :exec
UPDATE core.shipments
SET assigned_driver_id = sqlc.arg(assigned_driver_id)
WHERE id = sqlc.arg(id);

-- name: SetCancelled :exec
UPDATE core.shipments
SET status = 'CANCELLED',
    cancelled_at = sqlc.arg(cancelled_at)
WHERE id = sqlc.arg(id);
