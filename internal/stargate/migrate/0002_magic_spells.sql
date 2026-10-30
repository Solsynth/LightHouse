-- ---------------------------------------------------------------------------
-- Magic spells + affiliation spells (migrated from Passport into Stargate)
--   DysonNetwork.Passport/Migrations/20260401000000_InitialMigration.cs
--   (magic_spells, affiliation_spells, affiliation_results tables)
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- magic_spells (from Passport)
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS magic_spells CASCADE;
CREATE TABLE magic_spells (
    id         uuid NOT NULL,
    spell      varchar(1024) NOT NULL,
    type       integer NOT NULL,
    expires_at timestamp with time zone NULL,
    affected_at timestamp with time zone NULL,
    meta       jsonb NOT NULL DEFAULT '{}',
    account_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    CONSTRAINT pk_magic_spells PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_magic_spells_spell;
CREATE UNIQUE INDEX ix_magic_spells_spell ON magic_spells (spell, deleted_at);

-- ---------------------------------------------------------------------------
-- affiliation_spells (from Passport): registration invite / marketing usage,
-- distinct from the magic spells.
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS affiliation_spells CASCADE;
CREATE TABLE affiliation_spells (
    id         uuid NOT NULL,
    spell      varchar(1024) NOT NULL,
    type       integer NOT NULL,
    expires_at timestamp with time zone NULL,
    affected_at timestamp with time zone NULL,
    meta       jsonb NOT NULL DEFAULT '{}',
    account_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    CONSTRAINT pk_affiliation_spells PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_affiliation_spells_spell;
CREATE UNIQUE INDEX ix_affiliation_spells_spell ON affiliation_spells (spell, deleted_at);

-- ---------------------------------------------------------------------------
-- affiliation_results (from Passport): who used an affiliation spell.
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS affiliation_results CASCADE;
CREATE TABLE affiliation_results (
    id                  uuid NOT NULL,
    resource_identifier varchar(8192) NOT NULL,
    spell_id            uuid NOT NULL,
    created_at          timestamp with time zone NOT NULL,
    updated_at          timestamp with time zone NOT NULL,
    deleted_at          timestamp with time zone NULL,
    CONSTRAINT pk_affiliation_results PRIMARY KEY (id),
    CONSTRAINT fk_affiliation_results_affiliation_spells_spell_id FOREIGN KEY (spell_id) REFERENCES affiliation_spells (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_affiliation_results_spell_id;
CREATE INDEX ix_affiliation_results_spell_id ON affiliation_results (spell_id);
