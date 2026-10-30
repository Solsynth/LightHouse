-- Migration 0008: remove the in-app approval prompt marker.
-- The in-app notification factor (type 2) is no longer selectable: the
-- cross-device approval prompt is published for every challenge at creation,
-- so the per-challenge "prompt already requested" flag has no reader left.
ALTER TABLE auth_challenges DROP COLUMN IF EXISTS prompt_requested_at;
