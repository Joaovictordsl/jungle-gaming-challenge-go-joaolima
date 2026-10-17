CREATE TABLE wallets (
    id TEXT PRIMARY KEY,
    player_id TEXT NOT NULL,
    balance_cents BIGINT NOT NULL,
    currency TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_id_not_blank
        CHECK (id !~ '^[[:space:]]*$'),
    CONSTRAINT wallets_player_id_not_blank
        CHECK (player_id !~ '^[[:space:]]*$'),
    CONSTRAINT wallets_balance_nonnegative
        CHECK (balance_cents >= 0),
    CONSTRAINT wallets_currency_supported
        CHECK (currency IN ('BRL', 'USD')),
    CONSTRAINT wallets_version_positive
        CHECK (version >= 1),
    CONSTRAINT wallets_player_currency_unique
        UNIQUE (player_id, currency)
);
