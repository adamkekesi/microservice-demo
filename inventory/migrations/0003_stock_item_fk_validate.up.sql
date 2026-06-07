-- Phase 2: validate the NOT VALID cascade FK added in 0002. VALIDATE CONSTRAINT
-- scans existing stock rows but takes only a SHARE UPDATE EXCLUSIVE lock, so
-- concurrent reads AND writes keep flowing — unlike adding a validated FK in one
-- step. On an already-valid constraint (e.g. a DB that ran the pre-split 0002)
-- this is a fast no-op.
SET lock_timeout = '3s';
ALTER TABLE stock VALIDATE CONSTRAINT stock_item_id_fkey;
