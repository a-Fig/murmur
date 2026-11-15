-- How much a repo's devices wrote to their logs, in bytes (#550): the length of each record's event
-- as JSON, which for an encrypted event is the event inside the box. Like records it is exact
-- whether or not the bus is encrypted, and unlike them it grows with what the agents say, so it is
-- the measure of a bus's traffic that encryption doesn't hide. Rows from before this, and POSTs
-- from a census before it, have 0; the census reads each bus whole every day, so a day still on its
-- bus gets its bytes at the next run.
ALTER TABLE repo_days ADD COLUMN bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repo_census ADD COLUMN bytes INTEGER NOT NULL DEFAULT 0;
