# Synchronization protocol

`POST /v1/sync` combines an upload and incremental download in one PostgreSQL transaction. Clients always keep their own local database; the server is a replication target and restore source, not a requirement for timing solves.

## Client state

Persist these values locally:

- a stable, installation-specific `device.id` UUID;
- the last fully applied server `cursor`, initially `0`;
- a UUID and server `version` for every synchronized solve and session;
- an outbox of mutations, each with its own stable UUID.

Never generate a new mutation UUID when retrying an uncertain request. Reusing it makes retries idempotent.

## Sessions

Clients create, switch, close, archive, and automatically group sessions. The backend stores and synchronizes the resulting metadata but runs no automatic-session policy.

A session contains:

- client-generated UUID;
- name;
- event;
- kind: `manual` or `automatic`;
- start and optional end timestamp;
- archived state.

Solves may have no session. A referenced session must already exist or be included in the same request. Session mutations are applied before solve mutations.

## Mutations

An upsert mutation contains the last server version the client edited:

```json
{
  "id": "88b7bcec-31f7-4d95-a4aa-872e9b5ee536",
  "entity": "solve",
  "entity_id": "de478265-ae76-4bac-9bb1-f852b9f8f044",
  "operation": "upsert",
  "base_version": 0,
  "data": {
    "id": "de478265-ae76-4bac-9bb1-f852b9f8f044",
    "session_id": "1f83f1e1-af1e-48ac-af87-07c092360f51",
    "duration_ms": 12345,
    "penalty": "none",
    "solved_at": "2026-08-23T16:00:00Z",
    "scramble": "R U R' U'",
    "event": "3x3"
  }
}
```

Use `base_version: 0` for a new entity. For updates and deletes, use the version most recently applied locally. Delete mutations omit `data`.

Each outcome is one of:

- `accepted`: commit the returned version locally and remove the mutation from the outbox;
- `rejected`: fix or discard invalid local data based on `code`;
- `conflict`: the mutation was not applied; `current` contains the server record and version.

The server never silently resolves a conflict using a device clock. A client can keep the server record, keep its local edit by creating a new mutation against the returned version, or ask the user.

## Cursors and changes

The request includes the last applied cursor:

```json
{
  "cursor": 42,
  "device": {
    "id": "80084045-44cc-4341-ab0c-4fa46e545118",
    "name": "MacBook Pro",
    "platform": "macos"
  },
  "mutations": [],
  "limit": 1000
}
```

Changes are globally ordered per database and filtered to the authenticated user. Apply them in response order, in a local transaction. Advance the local cursor to `next_cursor` only after every returned change commits locally.

When `has_more` is true, immediately call sync again with `next_cursor`. An empty page preserves the supplied cursor.

The backend limits sync response payloads by size (default 512KB). A page may return fewer changes than requested if it hits the byte budget, but it will set `has_more` to true.

### Cursor Expiry
The server prunes old change rows that all devices have acknowledged, or that are older than the retention window (e.g., 90 days). 
If a client requests a `cursor` that is older than the oldest retained change, the server returns an HTTP 409 Conflict with code `cursor_expired`.
The client must discard its local cursor and sync state, and perform a full bootstrap using the snapshot endpoint.

## Protocol v2 Negotiation

Clients can opt into version 2 of the sync protocol by sending the `X-Sync-Protocol: 2` HTTP header. 
In v2, deletion changes and conflict payloads are minimized:
- `delete` changes only include `id`, `version`, and `deleted_at`.
- `conflict` outcomes omit the full entity data and instead return a `ConflictStub` with only `id`, `version`, and `updated_at`.

## Snapshot / Bootstrap Sync

`POST /v1/snapshot` is used for initial sync on a new device or when recovering from `cursor_expired`.
It returns the latest state of all entities, bypassing the change log history.

Request:
```json
{
  "device": { "id": "8008...", "name": "...", "platform": "..." },
  "cursor": 0,
  "entity": "session",
  "after_id": "00000000-0000-0000-0000-000000000000",
  "page_size": 500
}
```

Start with `entity: "session"` and a zero `after_id`. Keep requesting pages until `has_more` is false, then switch `entity` to `"solve"` and start again from a zero `after_id`. Each response returns `next_after_id` for the current entity's next page.

The `cursor` is a stable change-log watermark. Pass `0` on the very first request; echo the response `cursor` back on every later page. After the final snapshot page, save that cursor as your initial sync cursor and switch to `POST /v1/sync`. Because the cursor is fixed at bootstrap start, any changes that land while pages are being streamed are delivered by incremental sync afterwards.

## Server-Side Statistics

`GET /v1/stats` returns statistics computed directly by the server, eliminating the need to download all history to compute averages.
Supports optional `?event=3x3` filtering.

Returns totals, min/max/mean/stddev/total (excluding DNF, with +2 applied), and the current (most recent) Average of 5, 12, 50, and 100. DNF and +2 handling matches the CubeTimer client: more than one DNF in a window makes that average a DNF.

## History Endpoints

`GET /v1/sessions` and `GET /v1/sessions/{id}/solves` provide cursor-based pagination over a user's entire history.
Pass `?limit=50` and `?cursor=...` to paginate. The `next_cursor` and `has_more` are returned in the response.

## Recommended client loop

1. Store a new or edited solve/session locally and enqueue its mutation in the same local transaction.
2. When signed in and online, send up to 500 queued mutations with the current cursor.
3. Process mutation outcomes.
4. Apply ordered changes and the new cursor atomically.
5. Repeat while `has_more` is true or the outbox still contains retryable work.
6. Back off with jitter after network errors or HTTP `429`/`5xx`.

After first sign-in, enqueue all existing local sessions before their solves with `base_version: 0`. If the same UUID already exists remotely, the server returns a conflict instead of overwriting it.

## Deletions

Deletes are soft tombstones carrying a new version. Keep a local tombstone at least until its mutation is accepted and every returned change through the corresponding cursor is applied. A deleted session does not implicitly delete its solves.

## CubeTimer model mapping

Current Android mode mapping:

| Android value | API event |
| --- | --- |
| `CUBE_2x2` | `2x2` |
| `CUBE_3x3` | `3x3` |
| `CUBE_4x4` | `4x4` |
| `CUBE_5x5` | `5x5` |
| `MEGAMINX` | `megaminx` |
| `PYRAMINX` | `pyraminx` |

Penalty mapping:

| Android value | API penalty |
| --- | --- |
| `NONE` | `none` |
| `PLUS_TWO` | `plus_two` |
| `DNF` | `dnf` |

All timestamps use RFC 3339 UTC. Durations remain integer milliseconds. UUIDs are generated by clients so records keep the same identity while offline.
