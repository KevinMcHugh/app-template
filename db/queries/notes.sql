-- name: CreateNote :one
INSERT INTO notes (id, title, body, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetNote :one
SELECT * FROM notes
WHERE id = $1 AND deleted_at IS NULL;

-- One page of the notes, newest first, after the (created_at, id) of the
-- last note on the page before (app.Paginate). The sort ends in the id, so
-- the key is unique per row.
-- name: PageNotes :many
SELECT * FROM notes
WHERE deleted_at IS NULL
  AND (sqlc.narg(after_id)::text IS NULL OR (created_at, id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::text))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(lim);

-- Sets whichever of title and body are given (non-null) and leaves the
-- rest alone, so a PATCH can send one field.
-- name: UpdateNote :one
UPDATE notes SET
    title = COALESCE(sqlc.narg(title), title),
    body = COALESCE(sqlc.narg(body), body),
    updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeleteNote :execrows
UPDATE notes SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL;
