CREATE TABLE users (
    id            uuid PRIMARY KEY,
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL DEFAULT 'customer',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE signing_keys (
    id              uuid PRIMARY KEY,
    kid             text NOT NULL UNIQUE,
    private_key_pem text NOT NULL,
    public_key_pem  text NOT NULL,
    active          boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_signing_keys_active ON signing_keys (active);
