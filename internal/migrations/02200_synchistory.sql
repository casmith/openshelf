-- +goose Up
-- Library pages look up which connected account supplied an entry's source values.
create index sync_events_entry_idx on sync_events (library_entry_id) where library_entry_id is not null;
