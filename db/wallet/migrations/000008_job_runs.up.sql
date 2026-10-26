-- Every run of a scheduled job: when it ran, how it went, and what it found.
-- One run per job and slot, the time it was due: a replica that wrongly
-- thinks it leads still can't run the same slot twice.
CREATE TABLE job_runs (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job         text        NOT NULL,
    slot        timestamptz NOT NULL,
    status      text        NOT NULL DEFAULT 'running'
                            CHECK (status IN ('running', 'succeeded', 'failed')),
    summary     jsonb,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE (job, slot)
);
