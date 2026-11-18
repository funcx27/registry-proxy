package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMaintenanceGateRejectsWritesAndAllowsReads(t *testing.T) {
	gate := NewMaintenanceGate()
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	maintenanceStarted := make(chan struct{})
	releaseMaintenance := make(chan struct{})
	jobDone := make(chan error, 1)
	go func() {
		jobDone <- gate.RunExclusive(context.Background(), func() error {
			close(maintenanceStarted)
			<-releaseMaintenance
			return nil
		})
	}()
	<-maintenanceStarted

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v2/example/manifests/latest", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want %d", method, rec.Code, http.StatusServiceUnavailable)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/example/manifests/latest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	close(releaseMaintenance)
	if err := <-jobDone; err != nil {
		t.Fatalf("RunExclusive() error = %v", err)
	}
}

func TestMaintenanceGateWaitsForInFlightWrite(t *testing.T) {
	gate := NewMaintenanceGate()
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	writeDone := make(chan struct{})
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(writeStarted)
		<-releaseWrite
		close(writeDone)
		w.WriteHeader(http.StatusNoContent)
	}))

	go func() {
		req := httptest.NewRequest(http.MethodPut, "/v2/example/manifests/latest", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-writeStarted

	maintenanceStarted := make(chan struct{})
	jobDone := make(chan error, 1)
	go func() {
		jobDone <- gate.RunExclusive(context.Background(), func() error {
			close(maintenanceStarted)
			return nil
		})
	}()

	select {
	case <-maintenanceStarted:
		t.Fatal("maintenance started before the in-flight write finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseWrite)
	<-writeDone
	select {
	case <-maintenanceStarted:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start after the in-flight write finished")
	}
	if err := <-jobDone; err != nil {
		t.Fatalf("RunExclusive() error = %v", err)
	}
}

func TestMaintenanceGateCancellationRestoresWrites(t *testing.T) {
	gate := NewMaintenanceGate()
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-writeStarted:
		default:
			close(writeStarted)
		}
		<-releaseWrite
		w.WriteHeader(http.StatusNoContent)
	}))

	go func() {
		req := httptest.NewRequest(http.MethodPost, "/v2/example/blobs/uploads/", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-writeStarted

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := gate.RunExclusive(ctx, func() error {
		t.Fatal("maintenance callback must not run with a canceled context")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunExclusive() error = %v, want context.Canceled", err)
	}

	close(releaseWrite)

	req := httptest.NewRequest(http.MethodPost, "/v2/example/blobs/uploads/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST after canceled maintenance status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
