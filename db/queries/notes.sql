-- name: CreateNote :one
INSERT INTO notes (id, title, body, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetNote :one
SELECT * FROM notes
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListNotes :many
SELECT * FROM notes
WHERE deleted_at IS NULL
ORDER BY created_at DESC;

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
