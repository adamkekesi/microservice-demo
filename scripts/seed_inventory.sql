-- Inventory seed (Feature Spec §4.4): >=2 warehouses, >=3 items, and stock
-- rows so the shipment happy path runs immediately after boot.
-- Idempotent: safe to run repeatedly. Usage:
--   psql "$INVENTORY_DATABASE_URL" -f scripts/seed_inventory.sql

INSERT INTO warehouses (id, code, name) VALUES
    (gen_random_uuid(), 'WH-CENTRAL', 'Central Warehouse'),
    (gen_random_uuid(), 'WH-WEST',    'West Coast Warehouse')
ON CONFLICT (code) DO NOTHING;

INSERT INTO items (id, sku, name) VALUES
    (gen_random_uuid(), 'SKU-BOX-S', 'Small Box'),
    (gen_random_uuid(), 'SKU-BOX-M', 'Medium Box'),
    (gen_random_uuid(), 'SKU-BOX-L', 'Large Box')
ON CONFLICT (sku) DO NOTHING;

-- Give every (warehouse, item) pair 100 units on hand.
INSERT INTO stock (id, warehouse_id, item_id, quantity_on_hand)
SELECT gen_random_uuid(), w.id, i.id, 100
FROM warehouses w
CROSS JOIN items i
WHERE w.code IN ('WH-CENTRAL', 'WH-WEST')
  AND i.sku IN ('SKU-BOX-S', 'SKU-BOX-M', 'SKU-BOX-L')
ON CONFLICT (warehouse_id, item_id) DO NOTHING;
