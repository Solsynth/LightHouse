-- Migration 0007: Add per-account security mode (default=0, lockdown=1, lockoff=2).
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS security_mode integer NOT NULL DEFAULT 0;
