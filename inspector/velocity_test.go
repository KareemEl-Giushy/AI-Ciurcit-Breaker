package inspector

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestVelocityDetector_MaxRPS(t *testing.T) {
	vd := NewVelocityDetector(VelocityConfig{
		MaxRPS:             5.0,
		MaxEndpointRepeats: 100,
		RepeatWindow:       10 * time.Second,
		Enabled:            true,
	})

	sessionID := "user-session-1"
	endpoint := "/v1/chat/completions"

	// 5 requests within same instant -> all 5 allowed
	for i := 1; i <= 5; i++ {
		allowed, incident := vd.RecordRequest(sessionID, endpoint)
		if !allowed || incident != nil {
			t.Fatalf("request %d should have been allowed", i)
		}
	}

	// 6th request within same second -> must be blocked
	allowed, incident := vd.RecordRequest(sessionID, endpoint)
	if allowed || incident == nil {
		t.Fatalf("6th request within 1 second should have been blocked by MaxRPS")
	}

	if incident.FailureClass != ClassVelocityMaxRPS {
		t.Errorf("expected failure class %s, got %s", ClassVelocityMaxRPS, incident.FailureClass)
	}
}

func TestVelocityDetector_MaxEndpointRepeats(t *testing.T) {
	vd := NewVelocityDetector(VelocityConfig{
		MaxRPS:             0, // disabled
		MaxEndpointRepeats: 5,
		RepeatWindow:       2 * time.Second,
		Enabled:            true,
	})

	sessionID := "user-session-2"
	endpoint := "/api/v1/tools/execute"

	for i := 1; i <= 5; i++ {
		allowed, incident := vd.RecordRequest(sessionID, endpoint)
		if !allowed || incident != nil {
			t.Fatalf("request %d should have been allowed", i)
		}
	}

	// 6th request to same endpoint -> blocked
	allowed, incident := vd.RecordRequest(sessionID, endpoint)
	if allowed || incident == nil {
		t.Fatalf("6th request to same endpoint within window should have been blocked")
	}

	if incident.FailureClass != ClassVelocityMaxEndpointRepeats {
		t.Errorf("expected failure class %s, got %s", ClassVelocityMaxEndpointRepeats, incident.FailureClass)
	}

	// A different endpoint for the same session should still be allowed
	differentEndpoint := "/api/v1/health"
	diffAllowed, diffIncident := vd.RecordRequest(sessionID, differentEndpoint)
	if !diffAllowed || diffIncident != nil {
		t.Fatalf("different endpoint should have been allowed")
	}
}

func TestVelocityDetector_SessionIsolation(t *testing.T) {
	vd := NewVelocityDetector(VelocityConfig{
		MaxRPS:             2.0,
		MaxEndpointRepeats: 10,
		RepeatWindow:       5 * time.Second,
		Enabled:            true,
	})

	endpoint := "/v1/chat/completions"

	// Session A hits limit (2 requests)
	vd.RecordRequest("session-A", endpoint)
	vd.RecordRequest("session-A", endpoint)
	allowedA, _ := vd.RecordRequest("session-A", endpoint)
	if allowedA {
		t.Errorf("session-A should be blocked")
	}

	// Session B should still be allowed (isolated)
	allowedB, _ := vd.RecordRequest("session-B", endpoint)
	if !allowedB {
		t.Errorf("session-B should be allowed despite session-A being blocked")
	}
}

func TestVelocityDetector_Concurrency(t *testing.T) {
	vd := NewVelocityDetector(VelocityConfig{
		MaxRPS:             100.0,
		MaxEndpointRepeats: 200,
		RepeatWindow:       5 * time.Second,
		Enabled:            true,
	})

	var wg sync.WaitGroup
	workers := 20
	requestsPerWorker := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			session := fmt.Sprintf("session-%d", workerID%5)
			endpoint := fmt.Sprintf("/endpoint-%d", workerID%3)
			for j := 0; j < requestsPerWorker; j++ {
				_, _ = vd.RecordRequest(session, endpoint)
			}
		}(i)
	}

	wg.Wait()
}
