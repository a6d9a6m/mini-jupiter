package reservation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"
)

type fakeReconcilerRepo struct {
	findResults map[string]struct {
		rec   claimmodel.Record
		found bool
		err   error
	}
}

func (f fakeReconcilerRepo) FindClaimByIdempotency(_ context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error) {
	key := reconcileLookupKey(couponID, userID, idemKey)
	if res, ok := f.findResults[key]; ok {
		return res.rec, res.found, res.err
	}
	return claimmodel.Record{}, false, nil
}

type fakeReconcilerAdjudicator struct {
	leases        []hotpath.ReservationLease
	finalizeErrID map[string]error
	rollbackErrID map[string]error
	finalized     []string
	rolledBack    []string
}

func (f *fakeReconcilerAdjudicator) ListExpiredReservations(context.Context, time.Time, int) ([]hotpath.ReservationLease, error) {
	cp := make([]hotpath.ReservationLease, len(f.leases))
	copy(cp, f.leases)
	return cp, nil
}

func (f *fakeReconcilerAdjudicator) Finalize(_ context.Context, _ int64, _ int64, _ string, reservationID string, _ int64) error {
	if f.finalizeErrID != nil {
		if err := f.finalizeErrID[reservationID]; err != nil {
			return err
		}
	}
	f.finalized = append(f.finalized, reservationID)
	return nil
}

func (f *fakeReconcilerAdjudicator) Rollback(_ context.Context, _ int64, _ int64, _ string, reservationID string) error {
	if f.rollbackErrID != nil {
		if err := f.rollbackErrID[reservationID]; err != nil {
			return err
		}
	}
	f.rolledBack = append(f.rolledBack, reservationID)
	return nil
}

func TestReservationReconciler_LeaseFailureContinuesBatch(t *testing.T) {
	repo := fakeReconcilerRepo{
		findResults: map[string]struct {
			rec   claimmodel.Record
			found bool
			err   error
		}{
			reconcileLookupKey(1001, 2001, "idem-1"): {err: errors.New("mysql lookup failed")},
			reconcileLookupKey(1002, 2002, "idem-2"): {found: true, rec: claimmodel.Record{ID: 9002}},
			reconcileLookupKey(1003, 2003, "idem-3"): {found: false},
		},
	}
	adj := &fakeReconcilerAdjudicator{
		leases: []hotpath.ReservationLease{
			{ReservationID: "lease-1", CouponID: 1001, UserID: 2001, IdemKey: "idem-1"},
			{ReservationID: "lease-2", CouponID: 1002, UserID: 2002, IdemKey: "idem-2"},
			{ReservationID: "lease-3", CouponID: 1003, UserID: 2003, IdemKey: "idem-3"},
		},
	}
	reconciler, err := NewReservationReconciler(repo, adj, ReservationReconcilerConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new reservation reconciler failed: %v", err)
	}

	if err := reconciler.ReconcileOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("reconcile once failed: %v", err)
	}
	if len(adj.finalized) != 1 || adj.finalized[0] != "lease-2" {
		t.Fatalf("expected second lease finalized, got %+v", adj.finalized)
	}
	if len(adj.rolledBack) != 1 || adj.rolledBack[0] != "lease-3" {
		t.Fatalf("expected third lease rolled back, got %+v", adj.rolledBack)
	}
}

func TestReservationReconciler_FinalizeAndRollbackFailureContinueBatch(t *testing.T) {
	repo := fakeReconcilerRepo{
		findResults: map[string]struct {
			rec   claimmodel.Record
			found bool
			err   error
		}{
			reconcileLookupKey(1001, 2001, "idem-1"): {found: true, rec: claimmodel.Record{ID: 9001}},
			reconcileLookupKey(1002, 2002, "idem-2"): {found: false},
			reconcileLookupKey(1003, 2003, "idem-3"): {found: true, rec: claimmodel.Record{ID: 9003}},
		},
	}
	adj := &fakeReconcilerAdjudicator{
		leases: []hotpath.ReservationLease{
			{ReservationID: "lease-1", CouponID: 1001, UserID: 2001, IdemKey: "idem-1"},
			{ReservationID: "lease-2", CouponID: 1002, UserID: 2002, IdemKey: "idem-2"},
			{ReservationID: "lease-3", CouponID: 1003, UserID: 2003, IdemKey: "idem-3"},
		},
		finalizeErrID: map[string]error{"lease-1": errors.New("finalize failed")},
		rollbackErrID: map[string]error{"lease-2": errors.New("rollback failed")},
	}
	reconciler, err := NewReservationReconciler(repo, adj, ReservationReconcilerConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new reservation reconciler failed: %v", err)
	}

	if err := reconciler.ReconcileOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("reconcile once failed: %v", err)
	}
	if len(adj.finalized) != 1 || adj.finalized[0] != "lease-3" {
		t.Fatalf("expected third lease finalized after first failed, got %+v", adj.finalized)
	}
	if len(adj.rolledBack) != 0 {
		t.Fatalf("expected rollback failure lease not recorded as success, got %+v", adj.rolledBack)
	}
}

func reconcileLookupKey(couponID, userID int64, idemKey string) string {
	return fmt.Sprintf("%d:%d:%s", couponID, userID, idemKey)
}
