-- Durable trigger delivery. A started run is never automatically replayed:
-- a process crash cannot tell us whether an external tool already took effect.
CREATE TABLE IF NOT EXISTS trigger_deliveries (
    id BIGSERIAL PRIMARY KEY,
    trigger_id BIGINT NOT NULL REFERENCES agent_triggers(id) ON DELETE CASCADE,
    event_key TEXT NOT NULL,
    agent_key TEXT NOT NULL,
    title TEXT NOT NULL,
    message TEXT NOT NULL,
    task_id BIGINT NOT NULL DEFAULT 0,
    task_description TEXT NOT NULL DEFAULT '',
    task_goal TEXT NOT NULL DEFAULT '',
    mergeable BOOLEAN NOT NULL DEFAULT false,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','running','finished','interrupted','canceled')),
    conversation_id BIGINT REFERENCES conversations(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (trigger_id, event_key)
);
CREATE INDEX IF NOT EXISTS idx_trigger_deliveries_pending ON trigger_deliveries(id) WHERE state='pending';

-- Task IDs describe creation order, not timeout order. Track the last observed
-- occurrence per task so a revived task can time out again without losing it.
CREATE TABLE IF NOT EXISTS trigger_timeout_events (
    task_id BIGINT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    completed_at TIMESTAMPTZ NOT NULL
);

-- Archive/delete and cancellation share the task mutation's transaction.
CREATE OR REPLACE FUNCTION cancel_pending_task_triggers() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        UPDATE trigger_deliveries SET state='canceled',updated_at=now()
        WHERE task_id=OLD.id AND state='pending';
        RETURN OLD;
    END IF;
    IF NEW.deleted_at IS NOT NULL OR NEW.archived_at IS NOT NULL THEN
        UPDATE trigger_deliveries SET state='canceled',updated_at=now()
        WHERE task_id=NEW.id AND state='pending';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_tasks_cancel_pending_triggers ON tasks;
CREATE TRIGGER trg_tasks_cancel_pending_triggers
    BEFORE DELETE OR UPDATE OF deleted_at, archived_at ON tasks
    FOR EACH ROW EXECUTE FUNCTION cancel_pending_task_triggers();
