package services

import (
	"testing"
	"time"

	"pvfine/internal/pvf"
)

func TestArchiveTaskFollowUpDoesNotBlockClose(t *testing.T) {
	gate := newArchiveTaskGate()
	finish, ok := gate.tryBegin()
	if !ok {
		t.Fatal("open gate rejected follow-up")
	}
	waitFor := gate.beginClose()
	if len(waitFor) != 1 {
		t.Fatalf("close tracks %d tasks, want 1", len(waitFor))
	}
	if _, ok := gate.tryBegin(); ok {
		t.Fatal("closing gate accepted follow-up")
	}
	finish()
	<-waitFor[0]
	gate.endClose()
	finish, ok = gate.tryBegin()
	if !ok {
		t.Fatal("reopened gate rejected follow-up")
	}
	finish()
}

func TestCloseArchiveWaitsForArchiveTasks(t *testing.T) {
	c := NewCore()
	a := pvf.New()
	a.AddFile("data.bin", make([]byte, 2<<20), pvf.TypeScript)

	c.mu.Lock()
	c.archive = a
	c.mu.Unlock()

	finishTask := c.archiveTasks.begin()
	taskStarted := make(chan struct{})
	releaseTask := make(chan struct{})
	go func() {
		close(taskStarted)
		<-releaseTask
		finishTask()
	}()
	<-taskStarted

	closed := make(chan struct{})
	go func() {
		c.closeArchive()
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("closeArchive returned before the archive task exited")
	case <-time.After(50 * time.Millisecond):
	}

	c.mu.RLock()
	archiveCleared := c.archive == nil
	c.mu.RUnlock()
	if !archiveCleared {
		t.Fatal("closeArchive did not clear the active archive")
	}

	close(releaseTask)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closeArchive did not finish after the archive task exited")
	}
	if got := a.FileCount(); got != 0 {
		t.Fatalf("released archive still has %d files", got)
	}
}

func TestArchiveTaskGateWaitsDuringClose(t *testing.T) {
	gate := newArchiveTaskGate()
	gate.beginClose()
	started, finished := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		finish := gate.begin()
		finish()
		close(finished)
	}()
	<-started
	select {
	case <-finished:
		t.Fatal("task started while gate was closing")
	case <-time.After(20 * time.Millisecond):
	}
	gate.endClose()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("task did not resume after close")
	}
}
