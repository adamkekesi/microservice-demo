CREATE TABLE warehouses (
    id         uuid PRIMARY KEY,
    code       text NOT NULL UNIQUE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE items (
    id         uuid PRIMARY KEY,
    sku        text NOT NULL UNIQUE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE stock (
    id               uuid PRIMARY KEY,
    warehouse_id     uuid NOT NULL REFERENCES warehouses (id),
    item_id          uuid NOT NULL REFERENCES items (id),
    quantity_on_hand integer NOT NULL DEFAULT 0 CHECK (quantity_on_hand >= 0),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (warehouse_id, item_id)
);

CREATE TABLE reservations (
    id              uuid PRIMARY KEY,
    warehouse_id    uuid NOT NULL,
    item_id         uuid NOT NULL,
    quantity        integer NOT NULL CHECK (quantity > 0),
    status          text NOT NULL,
    idempotency_key text,
    reserved_by     uuid NOT NULL,
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Unique only when present, so multiple reservations may omit the key.
CREATE UNIQUE INDEX uq_reservations_idempotency_key
    ON reservations (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Supports the active-reservation availability query.
CREATE INDEX idx_reservations_active
    ON reservations (warehouse_id, item_id, status, expires_at);
