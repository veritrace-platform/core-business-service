//go:build integration

package shipment_test

import (
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// Shipments and participants are visible to participants only; only the owner adds or removes participants, and
// nobody deletes a shipment (ADR-0002).
func TestShipmentIsolation(t *testing.T) {
	w := newWorld(t)
	sh := w.db.CreateShipment(t, tenancytest.ShipmentSpec{
		Owner: w.owner, Carrier: w.carrier, Consignee: w.consignee, Lot: w.lot, Origin: w.plant, Destination: w.store, Quantity: 10,
	})
	shipmentRow := tenancytest.Rows{Table: "core.shipments", Where: map[string]any{"id": sh.ID}}
	participantRows := tenancytest.Rows{Table: "core.shipment_participants", Where: map[string]any{"shipment_id": sh.ID}}

	for _, participant := range []tenancytest.Tenant{w.owner, w.carrier, w.consignee} {
		w.db.AssertVisible(t, participant.ID, shipmentRow)
	}
	w.db.AssertHidden(t, w.stranger.ID, shipmentRow)
	w.db.AssertHidden(t, w.stranger.ID, participantRows)
	w.db.AssertHidden(t, tenancytest.NoTenant, shipmentRow)
	w.db.AssertReadOnly(t, w.consignee.ID, participantRows)

	w.db.AssertDenied(t, w.consignee.ID, `INSERT INTO core.shipment_participants (shipment_id, tenant_id, role, tenant_code, tenant_legal_name)
		VALUES ($1, $2, 'INSPECTOR', 'X', 'X')`, sh.ID, w.stranger.ID)
	w.db.AssertDenied(t, w.owner.ID, `DELETE FROM core.shipments WHERE id = $1`, sh.ID)
	w.db.AssertDenied(t, w.stranger.ID, `UPDATE core.shipment_participants SET tenant_code = 'X' WHERE shipment_id = $1`, sh.ID)
	// A tenant cannot create a shipment in another tenant's name.
	w.db.AssertDenied(t, w.stranger.ID, `
		INSERT INTO core.shipments (owner_tenant_id, sscc, lot_id, quantity, gtin, product_name, lot_number, expiration_date,
		    min_temp_celsius, max_temp_celsius, origin_location_id, origin_gln, origin_name, origin_latitude, origin_longitude,
		    origin_geo_fence_radius_meters, destination_location_id, destination_gln, destination_name, destination_latitude,
		    destination_longitude, destination_geo_fence_radius_meters, consignee_tenant_id, carrier_tenant_id, created_by)
		VALUES ($1, '999999999999999999', $2, 1, $3, 'X', 'X', current_date, 2, 8, $4, $5, 'X', 0, 0, 200,
		        $6, $7, 'X', 0, 0, 200, $8, $1, $9)`,
		w.owner.ID, w.lot.ID, w.product.GTIN, w.plant.ID, w.plant.GLN, w.store.ID, w.store.GLN, w.consignee.ID, w.owner.Admin.ID)
}
