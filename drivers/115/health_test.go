package _115

import (
	"errors"
	"testing"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestMarkUploadErrorRisk(t *testing.T) {
	mount := "/test-mount"
	riskErr := errors.New(`unexpected error: {"state":false,"message":"非法参数错误","code":990005}`)

	healthMu.Lock()
	delete(blocked, mount)
	uploadRiskFails[mount] = nil
	healthMu.Unlock()

	MarkUploadError(mount, riskErr)
	if IsStorageBlocked(mount) {
		t.Fatal("should not be blocked after 1 failure")
	}
	MarkUploadError(mount, riskErr)
	if IsStorageBlocked(mount) {
		t.Fatal("should not be blocked after 2 failures")
	}
	MarkUploadError(mount, riskErr)
	if !IsStorageBlocked(mount) {
		t.Fatal("should be blocked after 3 failures")
	}

	// 非上传风控错误不应触发风控
	healthMu.Lock()
	delete(blocked, mount)
	uploadRiskFails[mount] = nil
	healthMu.Unlock()
	MarkUploadError(mount, errors.New("some other network error"))
	if IsStorageBlocked(mount) {
		t.Fatal("should not be blocked by non-risk error")
	}

	// 达到阈值后计数已重置，需重新累计
	MarkUploadError(mount, riskErr)
	MarkUploadError(mount, riskErr)
	if IsStorageBlocked(mount) {
		t.Fatal("counter should reset after triggering blocked")
	}
}

func TestAuthFailureDoesNotEnterRiskCircuit(t *testing.T) {
	mount := "/test-auth-invalid"
	healthMu.Lock()
	delete(health, mount)
	delete(blocked, mount)
	healthMu.Unlock()

	MarkStorageError(mount, driver115.ErrNotLogin)
	entry, ok := GetStorageHealth(mount)
	if !ok || !entry.Invalid {
		t.Fatalf("auth failure health = %+v, %v; want invalid entry", entry, ok)
	}
	if IsStorageBlocked(mount) {
		t.Fatal("authentication failure must not be classified as provider risk")
	}
}
