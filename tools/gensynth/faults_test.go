package main

import (
	"testing"
	"time"
)

func TestCorruptUTR(t *testing.T) {
	original := "1568176960vxp0rj"
	corrupted := CorruptUTR(original)
	
	if corrupted == original {
		t.Errorf("Expected UTR to be mutated, got %s", corrupted)
	}
	if len(corrupted) >= len(original) {
		t.Errorf("Expected corrupted UTR to be shorter, got length %d", len(corrupted))
	}
}

func TestDelayPastSLA(t *testing.T) {
	baseTime := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	delayed := DelayPastSLA(baseTime)
	
	expectedDelay := 10 * 24 * time.Hour
	if delayed.Sub(baseTime) != expectedDelay {
		t.Errorf("Expected delay of 10 days, got %v", delayed.Sub(baseTime))
	}
}

func TestFatFingerAmount(t *testing.T) {
	originalAmount := int64(150000)
	corruptedAmount := FatFingerAmount(originalAmount)
	
	if corruptedAmount == originalAmount {
		t.Error("Expected amount to be altered")
	}
	if corruptedAmount != 149000 {
		t.Errorf("Expected fat-fingered amount to be 149000, got %d", corruptedAmount)
	}
}