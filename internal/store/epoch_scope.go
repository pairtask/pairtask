package store

// currentEpoch is a contact's current pairing epoch, from its one
// parameter, the contact: 1 for a key with no contact row. Rows written by
// an upsert into a table keyed by epoch land in it (#276).
const currentEpoch = `COALESCE((SELECT ce.pairing_epoch FROM peer_contacts ce WHERE ce.identity_key = ?), 1)`
