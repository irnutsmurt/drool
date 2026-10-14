-- name: InsertGroup :exec
INSERT INTO groups (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?);

-- name: GetGroup :one
SELECT * FROM groups WHERE id = ?;

-- name: ListGroups :many
SELECT * FROM groups ORDER BY name;

-- name: UpdateGroup :exec
UPDATE groups SET name = ?, description = ?, updated_at = ? WHERE id = ?;

-- name: DeleteGroup :exec
DELETE FROM groups WHERE id = ?;

-- name: ListGroupMembers :many
SELECT * FROM group_members ORDER BY group_id, host_id;

-- name: ListMembersOfGroup :many
SELECT host_id FROM group_members WHERE group_id = ?;

-- name: ListGroupsForHost :many
SELECT group_id FROM group_members WHERE host_id = ?;

-- name: DeleteGroupMembers :exec
DELETE FROM group_members WHERE group_id = ?;

-- name: DeleteHostGroupMemberships :exec
DELETE FROM group_members WHERE host_id = ?;

-- name: InsertGroupMember :exec
INSERT INTO group_members (group_id, host_id) VALUES (?, ?);

-- name: AddGroupMember :execrows
INSERT OR IGNORE INTO group_members (group_id, host_id) VALUES (?, ?);

-- name: RemoveGroupMember :execrows
DELETE FROM group_members WHERE group_id = ? AND host_id = ?;
