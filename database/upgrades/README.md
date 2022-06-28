## NOTE

Since we merged the schema update `2021-07-07-puppet-activity.go` (later moved to 00-latest-revision.sql), all upgrades brought in from upstream must be bumped by one version to avoid a clash.
Please do this when merging. As of writing this, we should always be one schema version ahead of mautrix/whatsapp.
