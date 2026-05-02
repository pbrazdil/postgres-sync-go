CREATE EXTENSION IF NOT EXISTS pgcrypto;

DROP SCHEMA IF EXISTS audit CASCADE;
DROP TABLE IF EXISTS partitioned_items CASCADE;
DROP TABLE IF EXISTS item_blocks CASCADE;
DROP TABLE IF EXISTS item_flag_audits CASCADE;
DROP TABLE IF EXISTS item_flags CASCADE;
DROP TABLE IF EXISTS items CASCADE;

CREATE SCHEMA audit;

CREATE TABLE items (
  id UUID PRIMARY KEY,
  value TEXT NOT NULL,
  priority INTEGER NOT NULL,
  archived BOOLEAN NOT NULL DEFAULT FALSE,
  category TEXT NOT NULL DEFAULT 'general',
  inserted_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE items REPLICA IDENTITY FULL;

INSERT INTO items (id, value, priority, archived, category, inserted_at) VALUES
  ('00000000-0000-0000-0000-000000000001', 'alpha', 1, FALSE, 'seed', '2025-01-01T00:00:00Z'),
  ('00000000-0000-0000-0000-000000000002', 'beta', 2, FALSE, 'seed', '2025-01-02T00:00:00Z'),
  ('00000000-0000-0000-0000-000000000003', 'gamma', 3, TRUE,  'archive', '2025-01-03T00:00:00Z');

CREATE TABLE item_flags (
  item_id UUID PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  enabled BOOLEAN NOT NULL DEFAULT FALSE
);

ALTER TABLE item_flags REPLICA IDENTITY FULL;

INSERT INTO item_flags (item_id, enabled) VALUES
  ('00000000-0000-0000-0000-000000000001', TRUE),
  ('00000000-0000-0000-0000-000000000002', FALSE),
  ('00000000-0000-0000-0000-000000000003', TRUE);

CREATE TABLE audit.flag_reasons (
  id INTEGER PRIMARY KEY,
  enabled BOOLEAN NOT NULL,
  label TEXT NOT NULL
);

ALTER TABLE audit.flag_reasons REPLICA IDENTITY FULL;

INSERT INTO audit.flag_reasons (id, enabled, label) VALUES
  (1, TRUE, 'trusted'),
  (2, FALSE, 'pending-review');

CREATE TABLE item_flag_audits (
  item_id UUID NOT NULL,
  reason_id INTEGER NOT NULL,
  approved BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (item_id, reason_id)
);

ALTER TABLE item_flag_audits REPLICA IDENTITY FULL;

INSERT INTO item_flag_audits (item_id, reason_id, approved) VALUES
  ('00000000-0000-0000-0000-000000000001', 1, TRUE),
  ('00000000-0000-0000-0000-000000000002', 2, TRUE),
  ('00000000-0000-0000-0000-000000000003', 1, TRUE);

CREATE TABLE item_blocks (
  item_id UUID PRIMARY KEY,
  reason TEXT NOT NULL
);

ALTER TABLE item_blocks REPLICA IDENTITY FULL;

INSERT INTO item_blocks (item_id, reason) VALUES
  ('00000000-0000-0000-0000-000000000003', 'archived-review');

CREATE TABLE partitioned_items (
  tenant_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (tenant_id, seq)
) PARTITION BY RANGE (seq);

CREATE TABLE partitioned_items_100
  PARTITION OF partitioned_items
  FOR VALUES FROM (0) TO (100);

CREATE TABLE partitioned_items_200
  PARTITION OF partitioned_items
  FOR VALUES FROM (100) TO (200);

ALTER TABLE partitioned_items REPLICA IDENTITY FULL;
ALTER TABLE partitioned_items_100 REPLICA IDENTITY FULL;
ALTER TABLE partitioned_items_200 REPLICA IDENTITY FULL;

INSERT INTO partitioned_items (tenant_id, seq, value) VALUES
  (1, 10, 'p-alpha'),
  (1, 20, 'p-beta'),
  (2, 120, 'p-gamma');
