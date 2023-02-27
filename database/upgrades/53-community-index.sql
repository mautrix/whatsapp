-- v53: Add index to make querying by community faster
CREATE INDEX portal_parent_group_idx ON portal(parent_group);
