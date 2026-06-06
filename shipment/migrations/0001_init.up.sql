CREATE TABLE shipments (
    id                  uuid PRIMARY KEY,
    owner_user_id       uuid NOT NULL,
    item_id             uuid NOT NULL,
    warehouse_id        uuid NOT NULL,
    quantity            integer NOT NULL CHECK (quantity > 0),
    destination_address text NOT NULL,
    status              text NOT NULL,
    reservation_id      uuid NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE shipment_status_history (
    id          uuid PRIMARY KEY,
    shipment_id uuid NOT NULL REFERENCES shipments (id),
    from_status text,
    to_status   text NOT NULL,
    reason      text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_shipments_owner ON shipments (owner_user_id);
CREATE INDEX idx_history_shipment ON shipment_status_history (shipment_id);
