-- Restore the 0001 FK without a delete action (and validated, as 0001 had it).
SET lock_timeout = '3s';
ALTER TABLE stock DROP CONSTRAINT IF EXISTS stock_item_id_fkey;
ALTER TABLE stock
    ADD CONSTRAINT stock_item_id_fkey
    FOREIGN KEY (item_id) REFERENCES items (id);
