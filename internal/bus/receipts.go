package bus

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// queryRower is satisfied by both *sql.DB and *sql.Tx, so checkReceipt can run
// either as a plain read or inside a transaction.
type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

// fingerprint hashes the canonical JSON of payload. encoding/json sorts map
// keys, so equivalent inputs with different key order hash the same.
func fingerprint(payload any) (string, error) {
	j, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(j)
	return hex.EncodeToString(h[:]), nil
}

func (b *Bus) checkReceipt(q queryRower, as, key string, payload any) (json.RawMessage, bool, error) {
	if key == "" {
		return nil, false, nil
	}
	var fp, result string
	err := q.QueryRow("SELECT fingerprint, result FROM receipts WHERE sender=? AND key=? AND expires_at > ?", as, key, b.nowMs()).Scan(&fp, &result)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, internal(err)
	}
	want, err := fingerprint(payload)
	if err != nil {
		return nil, false, internal(err)
	}
	if fp != want {
		return nil, false, errf("conflict", false, "idempotency_key %q was used with a different payload", key)
	}
	return json.RawMessage(result), true, nil
}

func (b *Bus) storeReceipt(tx *sql.Tx, as, key string, payload, result any) error {
	if key == "" {
		return nil
	}
	rj, err := json.Marshal(result)
	if err != nil {
		return err
	}
	fp, err := fingerprint(payload)
	if err != nil {
		return err
	}
	exp := b.nowMs() + int64(b.cfg.ReceiptRetentionMinutes)*60_000
	_, err = tx.Exec("INSERT OR REPLACE INTO receipts(sender,key,fingerprint,result,expires_at) VALUES(?,?,?,?,?)", as, key, fp, string(rj), exp)
	return err
}
