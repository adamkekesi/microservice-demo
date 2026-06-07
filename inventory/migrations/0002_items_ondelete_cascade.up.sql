-- Allow items to be deleted by cascading their stock rows. The 0001 FK
-- (stock.item_id -> items.id, Postgres-default name stock_item_id_fkey) had no
-- delete action, so deleting an item with stock failed; recreate it with
-- ON DELETE CASCADE so DELETE /items/{id} just works.
ALTER TABLE stock DROP CONSTRAINT IF EXISTS stock_item_id_fkey;
ALTER TABLE stock
    ADD CONSTRAINT stock_item_id_fkey
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE;
