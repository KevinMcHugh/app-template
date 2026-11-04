-- LockForWrite locks the live row kind/id names until the transaction ends
-- and returns when it last changed, for app.Guard to compare with what the
-- caller last saw. FOR UPDATE is not allowed beside UNION, so each kind
-- locks in its own CTE, and only the one whose kind matches reads a row.
-- A new kind that takes a guard gets a CTE here and an arm below.

-- name: LockForWrite :one
WITH
n AS (SELECT notes.updated_at FROM notes WHERE sqlc.arg(kind)::text = 'note' AND notes.id = sqlc.arg(target_id) AND notes.deleted_at IS NULL FOR UPDATE)
SELECT updated_at::timestamptz AS updated_at FROM (
    SELECT updated_at FROM n
) AS target
LIMIT 1;
