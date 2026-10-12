package audio

import (
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// The subprocess emits a readiness byte, then holds its output open until killed.
func TestCapturePipelineHelperProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] != "allrelay-capture-helper" {
		return
	}
	_, _ = os.Stdout.Write([]byte{1})
	time.Sleep(time.Hour)
	os.Exit(0)
}

func TestConcurrentCloseUnblocksReadAndReapsProcess(t *testing.T) {
	pipeline, err := NewCapturePipeline("test-capture", os.Args[0], []string{"-test.run=^TestCapturePipelineHelperProcess$", "--", "allrelay-capture-helper"})
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Close()
	ready := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(pipeline, make([]byte, 1))
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capture subprocess did not become ready")
	}

	readResult := make(chan error, 1)
	go func() {
		_, err := pipeline.Read(make([]byte, 1))
		readResult <- err
	}()
	var closes sync.WaitGroup
	for range 8 {
		closes.Add(1)
		go func() {
			defer closes.Done()
			_ = pipeline.Close()
		}()
	}
	closed := make(chan struct{})
	go func() {
		closes.Wait()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Close did not finish")
	}
	select {
	case err := <-readResult:
		if err == nil {
			t.Fatal("cancelled capture Read returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close left capture Read blocked")
	}
	if pipeline.cmd.ProcessState == nil {
		t.Fatal("Close returned before the subprocess was reaped")
	}
	select {
	case err := <-pipeline.Done():
		if err == nil {
			t.Fatal("killed capture subprocess reported successful completion")
		}
	default:
		t.Fatal("Close returned before subprocess completion was published")
	}
}
