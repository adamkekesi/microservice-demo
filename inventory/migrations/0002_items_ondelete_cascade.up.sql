-- Allow items to be deleted by cascading their stock rows. Phase 1 of an online,
-- non-blocking change: add the cascade FK as NOT VALID so it takes only a brief
-- lock and skips the full-table validation scan (new writes are enforced from
-- here on). Phase 2 (0003) validates existing rows under a weaker lock. The
-- lock_timeout makes the brief lock fail fast instead of queuing the whole table
-- behind it under load; the migrate Job retries (backoffLimit).
SET lock_timeout = '3s';
ALTER TABLE stock DROP CONSTRAINT IF EXISTS stock_item_id_fkey;
ALTER TABLE stock
    ADD CONSTRAINT stock_item_id_fkey
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE NOT VALID;
