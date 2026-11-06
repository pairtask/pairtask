BEGIN TRANSACTION;
CREATE TABLE agent_status (
    agent_id    TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    state       TEXT NOT NULL,
    task        TEXT NOT NULL,
    step        TEXT NOT NULL,
    waiting_on  TEXT NOT NULL,
    ref         TEXT NOT NULL,
    declared_at TEXT NOT NULL
);
CREATE TABLE agents (
    agent_id        TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    harness         TEXT,
    model           TEXT,
    connected_at    TIMESTAMP NOT NULL,
    disconnected_at TIMESTAMP,
    PRIMARY KEY (agent_id, session_id)
);
CREATE TABLE assignments (
    operator_message_id TEXT NOT NULL,
    agent_id            TEXT NOT NULL,
    task                TEXT NOT NULL,
    state               TEXT NOT NULL,
    assigned_at         TEXT NOT NULL,
    finished_at         TEXT,
    PRIMARY KEY (operator_message_id, agent_id)
);
CREATE TABLE claims (
    operator_message_id TEXT NOT NULL,
    task                TEXT NOT NULL,
    role                TEXT NOT NULL,
    agent_id            TEXT NOT NULL,
    topic               TEXT NOT NULL,
    state               TEXT NOT NULL,
    claimed_at          TEXT NOT NULL,
    finished_at         TEXT
);
CREATE TABLE contact_address_ops (
    message_id TEXT PRIMARY KEY,
    contact    TEXT NOT NULL,
    candidate  TEXT NOT NULL,
    state      TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    audit_id   INTEGER NOT NULL,
    at         TEXT NOT NULL
, generation INTEGER NOT NULL DEFAULT 0);
CREATE TABLE contact_pause_results (
    operation_id TEXT PRIMARY KEY,
    changed      INTEGER NOT NULL,
    generation   INTEGER NOT NULL,
    paused_at    TEXT NOT NULL
);
CREATE TABLE durable_agents (agent_id TEXT PRIMARY KEY, protocol_version INTEGER);
CREATE TABLE durable_inbox (
 message_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 received_at TIMESTAMP,
 acknowledged_at TIMESTAMP,
 PRIMARY KEY (message_id, agent_id)
);
CREATE TABLE durable_messages (
 message_id TEXT PRIMARY KEY,
 wire_bytes INTEGER NOT NULL,
 storage_bytes INTEGER NOT NULL
);
CREATE TABLE message_metadata (
    message_id TEXT PRIMARY KEY,
    seq INTEGER NOT NULL UNIQUE,
    sender_session TEXT NOT NULL,
    in_reply_to TEXT NOT NULL,
    ack_requested INTEGER NOT NULL
, origin TEXT, room TEXT, mentions TEXT, review_id TEXT, remote_contact TEXT, remote_sender TEXT, remote_reply_id TEXT, remote_reply_author INTEGER);
CREATE TABLE message_receipts (
    message_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    adapter_received_at TIMESTAMP,
    agent_acknowledged_at TIMESTAMP,
    PRIMARY KEY (message_id, agent_id)
);
CREATE TABLE messages (
    seq       INTEGER PRIMARY KEY,
    ts        TIMESTAMP NOT NULL,
    sender    TEXT NOT NULL,
    recipient TEXT NOT NULL,
    payload   TEXT NOT NULL
);
CREATE TABLE operator_audit (
 id INTEGER PRIMARY KEY,
 ts TIMESTAMP NOT NULL,
 action TEXT NOT NULL,
 principal_uid INTEGER,
 message_id TEXT,
 scope TEXT,
 result TEXT NOT NULL,
 detail TEXT
);
CREATE TABLE operator_contact_requests (
    operation_id TEXT PRIMARY KEY,
    op           TEXT NOT NULL,
    contact      TEXT NOT NULL,
    fingerprint  BLOB NOT NULL,
    created_at   TEXT NOT NULL
);
CREATE TABLE operator_requests (
 message_id TEXT PRIMARY KEY,
 scope_kind TEXT NOT NULL,
 members TEXT,
 recipients TEXT NOT NULL,
 excluded TEXT NOT NULL
);
CREATE TABLE paired_reply_outcomes (
    message_id    TEXT PRIMARY KEY,
    contact       TEXT NOT NULL,
    root_local_id TEXT NOT NULL,
    sender        TEXT NOT NULL,
    body_digest   TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    peer          TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT ''
);
CREATE TABLE paired_threads (
    local_id      TEXT PRIMARY KEY,
    contact       TEXT NOT NULL,
    root_local_id TEXT NOT NULL,
    peer_id       TEXT NOT NULL,
    peer_audience TEXT NOT NULL
);
CREATE TABLE peer_ack_floors (
    contact TEXT PRIMARY KEY,
    floor   INTEGER NOT NULL DEFAULT 0,
    expired INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE peer_agent_acks (
    contact    TEXT NOT NULL,
    message_id TEXT NOT NULL,
    agent      TEXT NOT NULL,
    read_at    TEXT NOT NULL,
    PRIMARY KEY (contact, message_id, agent)
);
CREATE TABLE peer_agent_reads (
    contact    TEXT NOT NULL,
    message_id TEXT NOT NULL,
    agent      TEXT NOT NULL,
    read_at    TEXT NOT NULL,
    state      TEXT NOT NULL,
    PRIMARY KEY (contact, message_id, agent)
);
CREATE TABLE peer_contacts (
    identity_key   TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    destination    TEXT NOT NULL,
    established_at TEXT NOT NULL,
    joined         INTEGER NOT NULL,
    invited        INTEGER NOT NULL,
    local_name     TEXT NOT NULL DEFAULT '',
    destination_at TEXT NOT NULL DEFAULT ''
, paused_at TEXT NOT NULL DEFAULT '', generation INTEGER NOT NULL DEFAULT 0, directory_ack TEXT, directory_ack_at TEXT, directory_at TEXT);
CREATE TABLE peer_directory (
    contact TEXT NOT NULL,
    agent   TEXT NOT NULL,
    seen_at TEXT NOT NULL,
    PRIMARY KEY (contact, agent)
);
CREATE TABLE peer_grants (
    contact    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    granted_at TEXT NOT NULL,
    PRIMARY KEY (contact, agent)
);
CREATE TABLE peer_identity (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    fingerprint TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE TABLE peer_joins (
    operation_id         TEXT PRIMARY KEY,
    joiner               TEXT NOT NULL,
    joiner_name          TEXT NOT NULL,
    invitation_id        TEXT NOT NULL DEFAULT '',
    inviter_operation_id TEXT NOT NULL DEFAULT '',
    peer                 TEXT NOT NULL DEFAULT '',
    transcript_id        BLOB,
    state                TEXT NOT NULL,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
, destination TEXT, decision_confirmed_at TEXT);
CREATE TABLE peer_message_floors (
    contact TEXT PRIMARY KEY,
    floor   INTEGER NOT NULL
);
CREATE TABLE peer_message_sequences (
    contact TEXT PRIMARY KEY,
    next    INTEGER NOT NULL
);
CREATE TABLE peer_messages (
    cursor       INTEGER PRIMARY KEY AUTOINCREMENT,
    contact      TEXT NOT NULL,
    direction    TEXT NOT NULL,
    message_id   TEXT NOT NULL,
    n            INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    text         TEXT,
    text_bytes   INTEGER NOT NULL,
    text_hash    BLOB NOT NULL,
    hash         BLOB NOT NULL,
    state        TEXT NOT NULL,
    class        TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    stored_at    TEXT NOT NULL,
    delivered_at TEXT,
    read_at      TEXT,
    receipt      TEXT NOT NULL DEFAULT '',
    receipt_at   TEXT, from_agent TEXT, to_agents TEXT, reply_id TEXT, reply_author INTEGER, local_id TEXT, review_ask TEXT, acked_at TEXT, ack_expired_at TEXT,
    UNIQUE (contact, direction, message_id),
    UNIQUE (contact, direction, n)
);
CREATE TABLE peer_pairing_outcomes (
    operation_id  TEXT PRIMARY KEY,
    invitation_id TEXT NOT NULL UNIQUE,
    identity_key  TEXT NOT NULL REFERENCES peer_pairing_requests(identity_key),
    name          TEXT NOT NULL,
    transcript_id BLOB NOT NULL,
    at            TEXT NOT NULL
);
CREATE TABLE peer_pairing_requests (
    identity_key    TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    state           TEXT NOT NULL,
    first_paired_at TEXT NOT NULL
, delivered_at TEXT);
CREATE TABLE peer_review_grants (
    contact    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    mode       TEXT NOT NULL CHECK (mode IN ('ask', 'auto')),
    publish    INTEGER NOT NULL,
    formal     INTEGER NOT NULL,
    granted_at TEXT NOT NULL,
    PRIMARY KEY (contact, agent)
);
CREATE TABLE refusal_summaries (
    contact     TEXT NOT NULL,
    agent       TEXT NOT NULL,
    pending     INTEGER NOT NULL DEFAULT 0,
    emitted_seq INTEGER NOT NULL DEFAULT 0,
    newest_at   TEXT,
    PRIMARY KEY (contact, agent)
);
CREATE TABLE remote_imports (
    contact      TEXT NOT NULL,
    remote_id    TEXT NOT NULL,
    local_id     TEXT NOT NULL UNIQUE,
    sender       TEXT NOT NULL,
    recipients   TEXT NOT NULL,
    reply_id     TEXT NOT NULL,
    reply_author INTEGER NOT NULL,
    state        TEXT NOT NULL,
    staged_at    TEXT NOT NULL,
    settled_at   TEXT, kind TEXT, approved_at TEXT, reason TEXT, body TEXT,
    PRIMARY KEY (contact, remote_id)
);
CREATE TABLE review_asks_out (
    contact         TEXT NOT NULL,
    ask_id          TEXT NOT NULL,
    reviewer        TEXT NOT NULL,
    requester_agent TEXT NOT NULL,
    local_id        TEXT NOT NULL,
    ask             TEXT NOT NULL,
    state           TEXT NOT NULL,
    seq             INTEGER NOT NULL DEFAULT 0,
    digest          BLOB,
    attempt         INTEGER NOT NULL DEFAULT 0,
    intent          INTEGER NOT NULL DEFAULT 0,
    verdict         TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    url             TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    settled_at      TEXT, review_id TEXT, findings TEXT, findings_seq INTEGER, findings_attempt INTEGER, findings_intent INTEGER, findings_digest BLOB, outcome_at TEXT,
    PRIMARY KEY (contact, ask_id)
);
CREATE TABLE review_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    review_id   TEXT NOT NULL,
    at          TEXT NOT NULL,
    actor       TEXT NOT NULL,
    action      TEXT NOT NULL,
    state       TEXT NOT NULL,
    attempt     INTEGER NOT NULL,
    publication TEXT NOT NULL,
    intent      INTEGER NOT NULL,
    base        TEXT,
    head        TEXT,
    outcome     TEXT,
    receipt     TEXT,
    detail      TEXT
);
CREATE TABLE review_outcome_seq (
    contact      TEXT NOT NULL,
    ask_id       TEXT NOT NULL,
    reviewer     TEXT NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    terminal_seq INTEGER,
    acked_seq    INTEGER NOT NULL DEFAULT 0,
    refused      TEXT,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (contact, ask_id)
);
CREATE TABLE review_outcomes_out (
    contact   TEXT NOT NULL,
    ask_id    TEXT NOT NULL,
    seq       INTEGER NOT NULL,
    payload   TEXT NOT NULL,
    queued_at TEXT NOT NULL,
    PRIMARY KEY (contact, ask_id)
);
CREATE TABLE reviews (
    review_id  TEXT PRIMARY KEY,
    reviewer   TEXT NOT NULL,
    state      TEXT NOT NULL,
    hash       TEXT NOT NULL,
    token      TEXT NOT NULL DEFAULT '',
    doc        TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT
);
CREATE TABLE room_members (
    room_id   TEXT NOT NULL,
    agent_id  TEXT NOT NULL,
    joined_at TEXT NOT NULL,
    PRIMARY KEY (room_id, agent_id)
);
CREATE TABLE rooms (
    id         TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
);
CREATE INDEX receipts_session ON message_receipts(session_id);
CREATE INDEX agents_recent ON agents(connected_at DESC, agent_id, session_id);
CREATE INDEX durable_pending ON durable_inbox(agent_id) WHERE acknowledged_at IS NULL;
CREATE UNIQUE INDEX claims_held ON claims(operator_message_id, task, role) WHERE state != 'released';
CREATE INDEX claims_by_message ON claims(operator_message_id, claimed_at);
CREATE INDEX room_members_by_agent ON room_members(agent_id, room_id);
CREATE INDEX reviews_by_reviewer ON reviews(reviewer, state);
CREATE INDEX reviews_by_update ON reviews(updated_at);
CREATE INDEX reviews_by_expiry ON reviews(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX review_events_by_review ON review_events(review_id, id);
CREATE INDEX peer_messages_state ON peer_messages (contact, direction, state);
CREATE INDEX remote_imports_pending ON remote_imports (state, staged_at);
DELETE FROM "sqlite_sequence";
COMMIT;
