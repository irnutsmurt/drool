-- Alerting (M12) uses raw SQL in internal/alerts; only what other
-- packages need is here.

-- name: ListNotifySecrets :many
SELECT id, kind, secret_ciphertext, secret_nonce, secret_key_version FROM notify_destinations
WHERE secret_ciphertext IS NOT NULL ORDER BY name;
