-- After M17 (dogfooding fix, ARCHITECTURE-ALPHA section 22): the host name an
-- administrator used for the web UI when starting a bulk conversion. Each
-- converted host is given the endpoint the controller resolves for it, the
-- same as installing one host from its page. '' (rollouts from before): the
-- controller resolves without it, as it did.
ALTER TABLE agent_rollouts ADD COLUMN web_host TEXT NOT NULL DEFAULT '';
