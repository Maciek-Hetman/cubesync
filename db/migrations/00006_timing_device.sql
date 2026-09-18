-- +goose Up
ALTER TABLE solves
  ADD COLUMN timing_device text NOT NULL DEFAULT 'keyboard'
  CHECK (timing_device IN ('keyboard', 'external_timer', 'smart_cube'));

-- +goose Down
ALTER TABLE solves DROP COLUMN IF EXISTS timing_device;
