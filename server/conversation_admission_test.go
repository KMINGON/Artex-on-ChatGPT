package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func conversationAdmissionServer(t *testing.T) (*Server, *db.Conversation) {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly configured isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, m: &Manager{pg: pg}, chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
	c, err := pg.CreateConversation("auto", "admission regression", nil)
	if err != nil {
		cancel()
		pg.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = pg.DeleteConversation(c.ID)
		pg.Close()
	})
	return s, c
}

func deleteAdmissionConversation(s *Server, id int64, batch bool) *httptest.ResponseRecorder {
	if batch {
		return retestRequest(s.pgDeleteConversationsBatch, http.MethodPost, 0, fmt.Sprintf(`{"ids":[%d]}`, id))
	}
	return retestRequest(s.pgDeleteConversation, http.MethodDelete, id, "")
}

func TestConversationAdmissionRejectsDeletedConversation(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			s, c := conversationAdmissionServer(t)
			if w := deleteAdmissionConversation(s, c.ID, batch); w.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			ctx, cancel := s.conversationRunContext(c.ID, s.convBusyKey(c.ID))
			defer cancel(nil)
			if ctx.Err() == nil {
				t.Fatal("a stale conversation was admitted after deletion")
			}
		})
	}
}

func TestConversationDeletionCancelsRegisteredAdmission(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			s, c := conversationAdmissionServer(t)
			ctx, cancel := s.conversationRunContext(c.ID, s.convBusyKey(c.ID))
			defer cancel(nil)
			if ctx.Err() != nil {
				t.Fatalf("existing conversation rejected: %v", context.Cause(ctx))
			}
			if w := deleteAdmissionConversation(s, c.ID, batch); w.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			if ctx.Err() == nil {
				t.Fatal("deletion did not cancel the registered run")
			}
		})
	}
}

func TestConversationAdmissionCannotPassDeletionInProgress(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			s, c := conversationAdmissionServer(t)
			// Hold DELETE after its cancellation lookup, before the row disappears.
			tx, err := s.m.pg.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`SELECT id FROM conversations WHERE id=$1 FOR UPDATE`, c.ID); err != nil {
				t.Fatal(err)
			}
			deleted := make(chan *httptest.ResponseRecorder, 1)
			go func() { deleted <- deleteAdmissionConversation(s, c.ID, batch) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var blocked bool
				if err := s.m.pg.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND query LIKE 'DELETE FROM conversations%'
 AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case w := <-deleted:
					t.Fatalf("delete did not reach the row-lock barrier: %d %s", w.Code, w.Body)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("delete did not reach the row-lock barrier")
				}
				time.Sleep(time.Millisecond)
			}
			type admission struct {
				ctx    context.Context
				cancel context.CancelCauseFunc
			}
			admitted := make(chan admission, 1)
			go func() {
				ctx, cancel := s.conversationRunContext(c.ID, s.convBusyKey(c.ID))
				admitted <- admission{ctx, cancel}
			}()
			select {
			case a := <-admitted:
				a.cancel(nil)
				t.Fatal("admission passed between delete cancellation and commit")
			case <-time.After(100 * time.Millisecond):
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-deleted:
				if w.Code != http.StatusOK {
					t.Fatalf("delete: %d %s", w.Code, w.Body)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("delete did not finish")
			}
			select {
			case a := <-admitted:
				defer a.cancel(nil)
				if a.ctx.Err() == nil {
					t.Fatal("deleted conversation admitted after the barrier released")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("admission did not finish")
			}
		})
	}
}
