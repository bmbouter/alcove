-- 029_catalog_items.sql
-- Individual items discovered within catalog sources and per-team enablement.

-- Individual items discovered within catalog sources
CREATE TABLE catalog_items (
    id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    source_id   TEXT NOT NULL,
    slug        TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT DEFAULT '',
    item_type   TEXT NOT NULL,
    definition  JSONB,
    source_file TEXT DEFAULT '',
    synced_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(source_id, slug)
);

CREATE INDEX idx_catalog_items_source ON catalog_items(source_id);
CREATE INDEX idx_catalog_items_type ON catalog_items(item_type);

-- Per-team enablement of individual catalog items
CREATE TABLE team_catalog_items (
    team_id     UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    source_id   TEXT NOT NULL,
    item_slug   TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    enabled_at  TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (team_id, source_id, item_slug)
);

CREATE INDEX idx_team_catalog_items_team ON team_catalog_items(team_id);
