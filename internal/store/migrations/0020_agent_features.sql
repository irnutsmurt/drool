-- M16: Shepherd/controller compatibility (ARCHITECTURE-ALPHA §33.8). The
-- optional capabilities a Shepherd announced in its last hello
-- ("firewall-1", "epoch-1", ...), so its compatibility with this controller
-- can be judged while it is offline too.
ALTER TABLE agent_identities ADD COLUMN features_json TEXT NOT NULL DEFAULT '[]';
