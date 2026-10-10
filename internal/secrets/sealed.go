package secrets

import (
	"context"

	"drool/internal/store/db"
)

// sealedLocation is one place where values sealed with the master key are
// stored. Every such place is listed in sealedLocations: master key checks
// (start, backup, restore), rotation and recovery all work from this list,
// so a new sealed column that isn't added here would be missed by all of
// them (TestSealedLocationsCoverSchema guards that).
type sealedLocation struct {
	Table string
	// Select returns id, AAD kind, ciphertext, nonce, key version and a
	// display label for every sealed value.
	Select string
	// Update re-seals one value: ciphertext, nonce, key version, id.
	Update string
}

var sealedLocations = []sealedLocation{
	{Table: "secrets",
		Select: `SELECT id, kind, ciphertext, nonce, key_version, 'credential ' || name FROM secrets`,
		Update: `UPDATE secrets SET ciphertext = ?, nonce = ?, key_version = ? WHERE id = ?`},
	{Table: "agent_ca",
		Select: `SELECT id, 'agent-ca-' || kind, key_ciphertext, key_nonce, key_version, 'Agent CA ' || kind || ' key'
			FROM agent_ca WHERE key_ciphertext IS NOT NULL`,
		Update: `UPDATE agent_ca SET key_ciphertext = ?, key_nonce = ?, key_version = ? WHERE id = ?`},
	{Table: "notify_destinations",
		Select: `SELECT id, 'notify-' || kind, secret_ciphertext, secret_nonce, secret_key_version, 'notification destination ' || name
			FROM notify_destinations WHERE secret_ciphertext IS NOT NULL`,
		Update: `UPDATE notify_destinations SET secret_ciphertext = ?, secret_nonce = ?, secret_key_version = ? WHERE id = ?`},
	{Table: "user_mfa",
		Select: `SELECT user_id, 'mfa-totp', secret_ciphertext, secret_nonce, secret_key_version,
			'two-factor secret of ' || coalesce((SELECT username FROM users WHERE users.id = user_mfa.user_id), user_id) FROM user_mfa`,
		Update: `UPDATE user_mfa SET secret_ciphertext = ?, secret_nonce = ?, secret_key_version = ? WHERE user_id = ?`},
	{Table: "audit_sink",
		Select: `SELECT id, 'audit-sink-header', secret_ciphertext, secret_nonce, secret_key_version, 'remote audit sink header'
			FROM audit_sink WHERE secret_ciphertext IS NOT NULL`,
		Update: `UPDATE audit_sink SET secret_ciphertext = ?, secret_nonce = ?, secret_key_version = ? WHERE id = ?`},
}

type sealedValue struct {
	ID, Kind   string
	Ciphertext []byte
	Nonce      []byte
	Version    int
	Label      string
}

func eachSealed(ctx context.Context, q db.DBTX, fn func(*sealedLocation, sealedValue) error) error {
	for i := range sealedLocations {
		loc := &sealedLocations[i]
		rows, err := q.QueryContext(ctx, loc.Select)
		if err != nil {
			return err
		}
		var vals []sealedValue
		for rows.Next() {
			var v sealedValue
			if err := rows.Scan(&v.ID, &v.Kind, &v.Ciphertext, &v.Nonce, &v.Version, &v.Label); err != nil {
				rows.Close()
				return err
			}
			vals = append(vals, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, v := range vals {
			if err := fn(loc, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// CountSealed returns how many values are sealed with the master key.
func CountSealed(ctx context.Context, q db.DBTX) (int, error) {
	n := 0
	err := eachSealed(ctx, q, func(*sealedLocation, sealedValue) error { n++; return nil })
	return n, err
}
