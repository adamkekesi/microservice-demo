-- Nothing to undo: a constraint's validated state isn't separately revertible
-- (rolling back the FK itself lives in 0002's down). No-op so golang-migrate has
-- a down file for this version.
SELECT 1;
