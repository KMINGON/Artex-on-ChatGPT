package chatgptauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestPlanGatePersistsPerRegistrationAndCatalogRemainsAvailable(t *testing.T) {
	f := setup(t)
	f.login(t, "quota-a")
	f.login(t, "quota-b")
	ctx, cancel, _, err := f.m.Authorize(t.Context(), "quota-a")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err = f.m.BlockPlan("quota-a", PlanUsageLimitCode); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("gate canceled already admitted inference")
	}
	if _, _, _, err = f.m.Authorize(t.Context(), "quota-a"); !errors.Is(err, ErrPlanBlocked) {
		t.Fatalf("gate: %v", err)
	}
	_, release, _, err := f.m.AuthorizeCatalog(t.Context(), "quota-a")
	if err != nil {
		t.Fatal(err)
	}
	release()
	_, release, _, err = f.m.Authorize(t.Context(), "quota-b")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err = f.m.BlockPlan("quota-b", "subscription_sharing_usage_unavailable"); err != nil {
		t.Fatal(err)
	}
	if f.record("quota-b").PlanBlockReason != "" {
		t.Fatal("transient latched gate")
	}
	if err = f.m.Close(); err != nil {
		t.Fatal(err)
	}
	f.m, err = newManager(f.dir, options{client: &http.Client{Transport: roundTripFunc(f.p.transport)}, now: func() time.Time { return time.Unix(f.now.Load(), 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = f.m.Authorize(t.Context(), "quota-a"); !errors.Is(err, ErrPlanBlocked) {
		t.Fatalf("restart gate: %v", err)
	}
	s := f.m.Snapshot()
	for _, a := range s.Accounts {
		if a.ID == "quota-a" && (!a.PlanBlocked || !a.Connected || !a.PlanEnabled) {
			t.Fatalf("gate erased OAuth state: %+v", a)
		}
	}
}

func TestPlanRecheckOneAuthorizationAndNewerLimitWins(t *testing.T) {
	f := setup(t)
	f.login(t, "quota-a")
	if err := f.m.BlockPlan("quota-a", PlanUsageLimitCode); err != nil {
		t.Fatal(err)
	}
	probe := func(ctx context.Context) error {
		_, release, _, err := f.m.Authorize(ctx, "quota-a")
		if err != nil {
			return err
		}
		release()
		if _, _, _, err = f.m.Authorize(ctx, "quota-a"); !errors.Is(err, ErrPlanBlocked) {
			t.Errorf("second inference permitted: %v", err)
		}
		if _, _, _, err = f.m.Authorize(t.Context(), "quota-a"); !errors.Is(err, ErrPlanBlocked) {
			t.Errorf("ordinary admission permitted: %v", err)
		}
		if err = f.m.Recheck(ctx, "quota-a", func(context.Context) error { return nil }); !errors.Is(err, ErrRecheckRunning) {
			t.Errorf("concurrent probe: %v", err)
		}
		return nil
	}
	if err := f.m.Recheck(t.Context(), "quota-a", func(ctx context.Context) error {
		if err := probe(ctx); err != nil {
			return err
		}
		return f.m.BlockPlan("quota-a", PlanUsageLimitCode)
	}); !errors.Is(err, ErrPlanBlocked) {
		t.Fatalf("newer quota overwritten: %v", err)
	}
	if err := f.m.Recheck(t.Context(), "quota-a", probe); err != nil {
		t.Fatal(err)
	}
	if f.record("quota-a").PlanBlockReason != "" {
		t.Fatal("successful explicit probe did not clear")
	}
	_, release, _, err := f.m.Authorize(t.Context(), "quota-a")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestPlanFailedRecheckDoesNotClearAndRefreshCannotEraseGate(t *testing.T) {
	f := setup(t)
	f.login(t, "quota-a")
	if err := f.m.BlockPlan("quota-a", PlanUsageLimitCode); err != nil {
		t.Fatal(err)
	}
	expected := errors.New("probe failed")
	if err := f.m.Recheck(t.Context(), "quota-a", func(ctx context.Context) error {
		_, release, _, e := f.m.Authorize(ctx, "quota-a")
		if e != nil {
			return e
		}
		release()
		return expected
	}); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if f.record("quota-a").PlanBlockReason == "" {
		t.Fatal("failed probe cleared gate")
	}
	f.now.Add(3600)
	started, proceed := make(chan struct{}), make(chan struct{})
	f.p.setToken(func(_ context.Context, form url.Values) (*http.Response, error) {
		close(started)
		<-proceed
		return jsonResponse(200, map[string]any{"access_token": "rotated", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": requestedScopes}), nil
	})
	result := make(chan error, 1)
	go func() {
		_, release, _, err := f.m.AuthorizeCatalog(t.Context(), "quota-a")
		if release != nil {
			release()
		}
		result <- err
	}()
	<-started
	if err := f.m.BlockPlan("quota-a", PlanUsageLimitCode); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if f.record("quota-a").PlanBlockReason != PlanUsageLimitCode {
		t.Fatal("refresh erased quota gate")
	}
}

func TestPlanGateSurvivesReauthenticationAndFailedClearPersistence(t *testing.T) {
	f := setup(t)
	f.login(t, "quota-a")
	if err := f.m.BlockPlan("quota-a", PlanUsageLimitCode); err != nil {
		t.Fatal(err)
	}
	q := f.start(t, "quota-a")
	token := f.p.sign(t, f.claims("quota-a", q.Get("nonce")))
	f.p.setToken(func(context.Context, url.Values) (*http.Response, error) {
		return jsonResponse(200, map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "id_token": token, "token_type": "Bearer", "expires_in": 3600, "scope": requestedScopes}), nil
	})
	if status, body := callback(t, q, url.Values{"code": {"new-code"}}); status != 200 {
		t.Fatalf("reauth=%d %s", status, body)
	}
	if f.record("quota-a").PlanBlockReason != PlanUsageLimitCode {
		t.Fatal("reauth erased gate")
	}
	oldDir := f.m.dir
	f.m.mu.Lock()
	f.m.dir = oldDir + "/missing/parent"
	f.m.mu.Unlock()
	err := f.m.Recheck(t.Context(), "quota-a", func(ctx context.Context) error {
		_, release, _, err := f.m.Authorize(ctx, "quota-a")
		if release != nil {
			release()
		}
		return err
	})
	f.m.mu.Lock()
	f.m.dir = oldDir
	f.m.mu.Unlock()
	if err == nil || f.record("quota-a").PlanBlockReason == "" {
		t.Fatalf("failed persistence cleared gate: %v", err)
	}
}
