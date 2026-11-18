package main

import (
	"testing"
	"time"
)

func TestLoginLimiterBoundsAnInstanceWithoutTrustingForwardedAddresses(t *testing.T) {
	var limiter requestWindow
	now := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		if !limiter.allow(now, 3) {
			t.Fatal("burst should fit the configured bound")
		}
	}
	if limiter.allow(now.Add(59*time.Second), 3) {
		t.Fatal("request above the window limit was admitted")
	}
	if !limiter.allow(now.Add(time.Minute), 3) {
		t.Fatal("new window should admit a request")
	}
}
