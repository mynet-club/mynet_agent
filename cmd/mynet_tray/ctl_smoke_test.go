package main

import "testing"

type stubCtrl struct{ started, stopped bool }

func (s *stubCtrl) Start() error     { s.started = true; return nil }
func (s *stubCtrl) Stop() error      { s.stopped = true; return nil }
func (s *stubCtrl) Describe() string { return "stub" }

func TestControllerStopStartDescribe(t *testing.T) {
	c := &stubCtrl{}
	if c.Describe() == "" {
		t.Fatal("Describe 空")
	}
	if err := c.Stop(); err != nil || !c.stopped {
		t.Fatal("Stop stub")
	}
	if err := c.Start(); err != nil || !c.started {
		t.Fatal("Start stub")
	}
}
