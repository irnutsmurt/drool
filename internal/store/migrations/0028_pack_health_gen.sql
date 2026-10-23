-- A Pack is part of monitoring's health configuration as soon as it is
-- created or renamed (ARCHITECTURE-ALPHA s47): its health checks and its
-- monitoring priority can be shown and set before it has a host. Deleting a
-- Pack and changing its members already counted (0013).
CREATE TRIGGER mon_health_gen_grp_ins AFTER INSERT ON groups BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_grp_upd AFTER UPDATE OF name ON groups BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
