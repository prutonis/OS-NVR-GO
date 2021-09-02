// Copyright 2020-2021 The OS-NVR Authors.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation; version 2.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"nvr/pkg/ffmpeg"
	"nvr/pkg/ffmpeg/ffmock"
	"nvr/pkg/log"
	"nvr/pkg/storage"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type cancelFunc func()

func prepareDir(t *testing.T) (string, cancelFunc) {
	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	configDir := tempDir + "/monitors"

	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}

	err = filepath.Walk("./testdata/monitors/", func(path string, info os.FileInfo, _ error) error {
		if !info.IsDir() {
			file, err := ioutil.ReadFile(path)
			if err != nil {
				return err
			}
			if err := ioutil.WriteFile(configDir+"/"+info.Name(), file, 0600); err != nil {
				return err
			}

		}
		return nil
	})
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatal(err)
	}

	cancel := func() {
		os.RemoveAll(tempDir)
	}
	return configDir, cancel
}

func newTestManager(t *testing.T) (string, *Manager, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.NewLogger()
	go logger.Start(ctx)

	configDir, cancel2 := prepareDir(t)

	cancelFunc := func() {
		cancel()
		cancel2()
	}

	manager, err := NewManager(
		configDir,
		&storage.ConfigEnv{},
		logger,
		Hooks{},
	)
	if err != nil {
		t.Fatal(err)
	}

	return configDir, manager, cancelFunc
}

func readConfig(path string) (Config, error) {
	file, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if json.Unmarshal(file, &config); err != nil {
		return nil, err
	}
	return config, nil
}

func TestNewManager(t *testing.T) {
	t.Run("working", func(t *testing.T) {
		configDir, manager, cancel := newTestManager(t)
		defer cancel()

		config, err := readConfig(configDir + "/1.json")
		if err != nil {
			t.Fatalf("%v", err)
		}

		expected := fmt.Sprintf("%v", config)
		actual := fmt.Sprintf("%v", manager.Monitors["1"].Config)

		if expected != actual {
			t.Fatalf("expected: %v, got %v", expected, actual)
		}
	})
	t.Run("readFileErr", func(t *testing.T) {
		_, err := NewManager(
			"/dev/null/nil.json",
			&storage.ConfigEnv{},
			&log.Logger{},
			Hooks{},
		)

		if err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("unmarshalErr", func(t *testing.T) {
		configDir, cancel := prepareDir(t)
		defer cancel()

		data := []byte("{")
		if err := ioutil.WriteFile(configDir+"/1.json", data, 0600); err != nil {
			t.Fatalf("%v", err)
		}

		_, err := NewManager(
			configDir,
			&storage.ConfigEnv{},
			&log.Logger{},
			Hooks{},
		)

		if err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})

}

func TestMonitorSet(t *testing.T) {
	t.Run("createNew", func(t *testing.T) {
		configDir, manager, cancel := newTestManager(t)
		defer cancel()

		config := manager.Monitors["1"].Config
		config["name"] = "new"
		err := manager.MonitorSet("new", config)
		if err != nil {
			t.Fatalf("%v", err)
		}

		newName := manager.Monitors["new"].Config["name"]
		if newName != "new" {
			t.Fatalf("expected: new, got: %v", newName)
		}

		// Check if changes were saved to file.
		config, err = readConfig(configDir + "/new.json")
		if err != nil {
			t.Fatalf("%v", err)
		}

		expected := fmt.Sprintf("%v", manager.Monitors["new"].Config)
		actual := fmt.Sprintf("%v", config)

		if expected != actual {
			t.Fatalf("expected: %v, got %v", expected, actual)
		}
	})
	t.Run("setOld", func(t *testing.T) {
		configDir, manager, cancel := newTestManager(t)
		defer cancel()

		oldMonitor := manager.Monitors["1"]
		oldMonitor.running = true

		oldname := oldMonitor.Config["name"]
		if oldname != "one" {
			t.Fatalf("expected: one, got: %v", oldname)
		}

		config := oldMonitor.Config
		config["name"] = "two"
		err := manager.MonitorSet("1", config)
		if err != nil {
			t.Fatalf("%v", err)
		}

		if !manager.Monitors["1"].running {
			t.Fatal("old monitor was reset")
		}

		newName := manager.Monitors["1"].Config["name"]
		if newName != "two" {
			t.Fatalf("expected: two, got: %v", newName)
		}

		// Check if changes were saved to file.
		config, err = readConfig(configDir + "/1.json")
		if err != nil {
			t.Fatalf("%v", err)
		}

		expected := fmt.Sprintf("%v", manager.Monitors["1"].Config)
		actual := fmt.Sprintf("%v", config)

		if expected != actual {
			t.Fatalf("expected: %v, got %v", expected, actual)
		}
	})
	t.Run("writeFileErr", func(t *testing.T) {
		_, manager, cancel := newTestManager(t)
		defer cancel()

		manager.path = "/dev/null"
		if err := manager.MonitorSet("1", Config{}); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
}

func TestMonitorDelete(t *testing.T) {
	t.Run("working", func(t *testing.T) {
		_, manager, cancel := newTestManager(t)
		defer cancel()

		if _, exists := manager.Monitors["1"]; !exists {
			t.Fatal("test monitor does not exist")
		}

		if err := manager.MonitorDelete("1"); err != nil {
			t.Fatalf("%v", err)
		}

		if _, exists := manager.Monitors["1"]; exists {
			t.Fatal("monitor was not deleted")
		}
	})
	t.Run("existErr", func(t *testing.T) {
		_, manager, cancel := newTestManager(t)
		defer cancel()

		if err := manager.MonitorDelete("nil"); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("removeErr", func(t *testing.T) {
		_, manager, cancel := newTestManager(t)
		defer cancel()

		manager.path = "/dev/null"

		if err := manager.MonitorDelete("1"); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
}
func TestMonitorList(t *testing.T) {
	_, manager, cancel := newTestManager(t)
	defer cancel()

	expected := "map[1:map[audioEnabled:true enable:false id:1 name:one subInputEnabled:false]" +
		" 2:map[audioEnabled:false enable:false id:2 name:two subInputEnabled:true]]"

	actual := fmt.Sprintf("%v", manager.MonitorList())
	if actual != expected {
		t.Fatalf("\nexpected:\n%v.\ngot:\n%v.", expected, actual)
	}
}

func TestMonitorConfigs(t *testing.T) {
	_, manager, cancel := newTestManager(t)
	defer cancel()

	expected := "map[1:map[audioEncoder:copy enable:false id:1 mainInput:x1 name:one]" +
		" 2:map[enable:false id:2 name:two subInput:x2]]"

	actual := fmt.Sprintf("%v", manager.MonitorConfigs())
	if actual != expected {
		t.Fatalf("\nexpected:\n%v.\ngot:\n%v", expected, actual)
	}
}

func TestStopAllMonitors(t *testing.T) {
	runningMonitor := func() *Monitor {
		return &Monitor{
			running: true,
			WG:      &sync.WaitGroup{},
			cancel:  func() {},
		}
	}
	m := Manager{
		Monitors: map[string]*Monitor{
			"1": runningMonitor(),
			"2": runningMonitor(),
		},
	}
	if !m.Monitors["1"].running || !m.Monitors["2"].running {
		t.Fatal("monitors are not running")
	}
	m.StopAll()
	if m.Monitors["1"].running || m.Monitors["2"].running {
		t.Fatal("monitors did not stop")
	}
}

func mockWaitForKeyframe(_ context.Context, _ string) (time.Duration, error) {
	return 0, nil
}

func newTestMonitor(t *testing.T) (*Monitor, context.Context, func()) {
	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("could not create temp dir: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	logger := log.NewLogger()
	go logger.Start(ctx)

	cancelFunc := func() {
		cancel()
		os.RemoveAll(tempDir)
	}

	m := &Monitor{
		Env: &storage.ConfigEnv{
			SHMDir:     tempDir,
			StorageDir: tempDir + "/storage",
		},
		Config: map[string]string{
			"enable":          "true",
			"videoLength":     "0.0003", // 18ms
			"timestampOffset": "0",
		},
		Trigger:  make(Trigger),
		eventsMu: &sync.Mutex{},
		running:  true,

		hooks:               mockHooks,
		runInputProcess:     mockRunInputProcess,
		startRecording:      mockStartRecording,
		runRecordingProcess: mockRunRecordingProcess,
		newProcess:          ffmock.NewProcess,
		sizeFromStream:      mockSizeFromStream,
		waitForKeyframe:     mockWaitForKeyframe,
		videoDuration:       mockVideoDuration,
		watchdogInterval:    10 * time.Second,

		WG:  &sync.WaitGroup{},
		Log: logger,
	}
	return m, ctx, cancelFunc
}

func TestStartMonitor(t *testing.T) {
	t.Run("runningErr", func(t *testing.T) {
		m := Monitor{running: true}
		if err := m.Start(); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("disabled", func(t *testing.T) {
		m, _, cancel := newTestMonitor(t)
		defer cancel()

		m.running = false
		m.Config = map[string]string{"name": "test"}

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		go func() {
			if err := m.Start(); err != nil {
				t.Fatalf("%v", err)
			}
		}()

		expected := "test: disabled\n"
		actual := <-feed
		if actual != expected {
			t.Fatalf("expected: %v, got: %v", expected, actual)
		}
	})
	t.Run("tmpDirErr", func(t *testing.T) {
		m, _, cancel := newTestMonitor(t)
		defer cancel()

		m.running = false
		m.Env.SHMDir = "/dev/null"

		if err := m.Start(); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
}

func mockRunInputProcess(context.Context, *Monitor, bool) error {
	return nil
}
func mockRunInputProcessErr(context.Context, *Monitor, bool) error {
	return errors.New("mock")
}

func TestStartMainProcess(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		ctx, cancel2 := context.WithCancel(context.Background())
		cancel2()

		feed, cancel3 := m.Log.Subscribe()
		defer cancel3()

		m.WG.Add(1)
		go m.startInputProcess(ctx, false)

		actual := <-feed
		expected := ": main process: stopped\n"

		if actual != expected {
			t.Fatalf("\nexpected: \n%v \ngot: \n%v", expected, actual)
		}
	})
	t.Run("crashed", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.runInputProcess = mockRunInputProcessErr

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		m.WG.Add(1)
		go m.startInputProcess(ctx, true)

		actual := <-feed
		expected := ": sub process: crashed: mock\n"

		if actual != expected {
			t.Fatalf("\nexpected: \n%v \ngot: \n%v", expected, actual)
		}
	})
}

func TestRunInputProcess(t *testing.T) {
	t.Run("sizeFromStream", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		if err := runInputProcess(ctx, m, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		actual := m.Config["sizeMain"]

		expected := "123x456"
		if actual != expected {
			t.Fatalf("expected: %v, got: %v", expected, actual)
		}
	})
	t.Run("sizeFromStreamErr", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.sizeFromStream = mockSizeFromStreamErr

		if err := runInputProcess(ctx, m, false); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("stopped", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.newProcess = ffmock.NewProcessNil

		go func() {
			if err := runInputProcess(ctx, m, true); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}()

		time.Sleep(10 * time.Millisecond)
		m.Stop()

		if m.running {
			t.Fatal("monitor did not stop")
		}
	})
	t.Run("crashed", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.newProcess = ffmock.NewProcessErr

		if err := runInputProcess(ctx, m, false); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
}

func TestWatchdog(t *testing.T) {
	t.Run("freeze", func(t *testing.T) {
		mu := &sync.Mutex{}
		onStop := func() {
			mu.Unlock()
		}
		mocker := ffmock.NewProcessMocker(ffmock.MockProcessConfig{
			OnStop: onStop,
		})

		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.Config["id"] = "id"
		m.watchdogInterval = 10 * time.Millisecond

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		if err := os.MkdirAll(m.hlsPath(), 0700); err != nil {
			t.Fatal(err)
		}

		mu.Lock()
		go m.startWatchdog(ctx, mocker(&exec.Cmd{}), "x")

		mu.Lock()
		mu.Unlock()

		actual := <-feed
		expected := ": x process: watchdog: possible freeze detected, restarting\n"

		if actual != expected {
			t.Fatalf("expected: %v, got: %v", expected, actual)
		}
	})
	t.Run("fileErr", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.Config["id"] = "id"
		m.watchdogInterval = 10 * time.Millisecond

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		go m.startWatchdog(ctx, ffmock.NewProcess(&exec.Cmd{}), "x")

		actual := <-feed
		expected := ": x process: watchdog: no such file or directory\n"

		if actual != expected {
			t.Fatalf("expected: %v, got: %v", expected, actual)
		}
	})
}

func TestStartRecorder(t *testing.T) {
	t.Run("missingTime", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.startRecording = mockStartRecording
		m.WG.Add(1)

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		go m.startRecorder(ctx)
		m.Trigger <- Event{RecDuration: 1}
		actual := <-feed

		expected := `: recoder: invalid event: missing 'Time', event:
 Time: 0001-01-01 00:00:00 +0000 UTC
 Detections: []
 Duration: 0s
 RecDuration: 1ns
`

		if actual != expected {
			t.Fatalf("\nexpected: \n%v. \ngot: \n%v.", expected, actual)
		}
	})
	t.Run("missingRecDuration", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.startRecording = mockStartRecording
		m.WG.Add(1)

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		go m.startRecorder(ctx)
		m.Trigger <- Event{Time: (time.Unix(1, 0).UTC())}
		actual := <-feed

		expected := `: recoder: invalid event: missing 'RecDuration', event:
 Time: 1970-01-01 00:00:01 +0000 UTC
 Detections: []
 Duration: 0s
 RecDuration: 0s
`

		if actual != expected {
			t.Fatalf("\nexpected: \n%v. \ngot: \n%v.", expected, actual)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.startRecording = mockStartRecording

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		go m.startRecorder(ctx)
		m.Trigger <- Event{
			Time:        time.Now().Add(time.Duration(-1) * time.Hour),
			RecDuration: 1,
		}

		actual := <-feed

		expected := ": recorder: trigger reached end, stopping recording\n"

		if actual != expected {
			t.Fatalf("\nexpected: \n%v \ngot: \n%v", expected, actual)
		}
	})
	t.Run("timeoutUpdate", func(t *testing.T) {
		mu := sync.Mutex{}
		mockStartRecording := func(context.Context, *Monitor) {
			mu.Unlock()
		}

		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.WG.Add(1)
		m.startRecording = mockStartRecording

		ctx, cancel2 := context.WithCancel(context.Background())
		defer cancel2()

		mu.Lock()
		go m.startRecorder(ctx)

		now := time.Now()
		m.Trigger <- Event{Time: now, RecDuration: 10 * time.Millisecond}
		m.Trigger <- Event{Time: now, RecDuration: 50 * time.Millisecond}

		mu.Lock()
		mu.Unlock()
	})
	t.Run("recordingCheck", func(t *testing.T) {
		mu := sync.Mutex{}
		mockStartRecording := func(context.Context, *Monitor) {
			mu.Unlock()
		}

		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.startRecording = mockStartRecording

		ctx, cancel2 := context.WithCancel(context.Background())
		defer cancel2()

		mu.Lock()
		go m.startRecorder(ctx)

		now := time.Now()
		m.Trigger <- Event{Time: now, RecDuration: 10 * time.Millisecond}
		m.Trigger <- Event{Time: now, RecDuration: 11 * time.Millisecond}
		m.Trigger <- Event{Time: now, RecDuration: 0 * time.Millisecond}

		mu.Lock()
		mu.Unlock()
	})
}

func mockStartRecording(context.Context, *Monitor) {}

func TestStartRecording(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		m, _, cancel := newTestMonitor(t)
		defer cancel()

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		// Cancel the recording not the monitor.
		ctx2, cancel3 := context.WithCancel(context.Background())
		cancel3()

		m.WG.Add(1)
		go startRecording(ctx2, m)

		expected := ": recording stopped\n"

		actual := <-feed
		if actual != expected {
			t.Fatalf("\nexpected: \n%v \ngot: \n%v", expected, actual)
		}
	})
	t.Run("crashed", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.runRecordingProcess = mockRunRecordingProcessErr

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		m.WG.Add(1)
		go startRecording(ctx, m)

		expected := ": recording process: mock\n"

		actual := <-feed
		if actual != expected {
			t.Fatalf("\nexpected: \n%v \ngot: \n%v", expected, actual)
		}
	})
}

func mockRunRecordingProcess(context.Context, *Monitor) error {
	return nil
}

func mockRunRecordingProcessErr(context.Context, *Monitor) error {
	return errors.New("mock")
}

func TestRunRecordingProcess(t *testing.T) {
	t.Run("finished", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.newProcess = ffmock.NewProcessNil

		feed, cancel2 := m.Log.Subscribe()
		defer cancel2()

		go func() {
			if err := runRecordingProcess(ctx, m); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}()

		<-feed
		<-feed
		actual := <-feed

		expected := ": recording finished\n"

		if actual != expected {
			t.Fatalf("\nexpected: \n%v \ngot: \n%v", expected, actual)
		}
	})
	t.Run("waitForKeyframeErr", func(t *testing.T) {
		mockWaitForKeyframeErr := func(_ context.Context, _ string) (time.Duration, error) {
			return 0, errors.New("mock")
		}

		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.newProcess = ffmock.NewProcess
		m.waitForKeyframe = mockWaitForKeyframeErr

		m.WG.Add(1)
		if err := runRecordingProcess(ctx, m); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("mkdirErr", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.Env = &storage.ConfigEnv{
			StorageDir: "/dev/null",
		}

		if err := runRecordingProcess(ctx, m); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("genArgsErr", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.Config["videoLength"] = ""

		if err := runRecordingProcess(ctx, m); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("parseOffsetErr", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()

		m.Config["timestampOffset"] = ""

		if err := runRecordingProcess(ctx, m); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("crashed", func(t *testing.T) {
		m, ctx, cancel := newTestMonitor(t)
		defer cancel()
		m.newProcess = ffmock.NewProcessErr

		if err := runRecordingProcess(ctx, m); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
}

func mockSizeFromStream(string) (string, error) {
	return "123x456", nil
}
func mockSizeFromStreamErr(string) (string, error) {
	return "", errors.New("mock")
}

var mockHooks = Hooks{
	Start:     func(context.Context, *Monitor) {},
	StartMain: func(context.Context, *Monitor, *string) {},
	StartSub:  func(context.Context, *Monitor, *string) {},
	RecSave:   func(*Monitor, *string) {},
}

func TestGenInputArgs(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		m := &Monitor{
			Env: &storage.ConfigEnv{},
			Config: map[string]string{
				"logLevel":     "1",
				"mainInput":    "2",
				"audioEncoder": "3",
				"videoEncoder": "4",
				"id":           "id",
			},
		}
		actual := generateInputArgs(m, false)
		expected := "-loglevel 1 -i 2 -c:a 3 -c:v 4 -preset veryfast -f hls -hls_flags" +
			" delete_segments -hls_list_size 2 -hls_allow_cache 0 /hls/id/id.m3u8"
		if actual != expected {
			t.Fatalf("\nexpected: \n%v \ngot \n%v", expected, actual)
		}
	})
	t.Run("hwaccel", func(t *testing.T) {
		m := &Monitor{
			Env: &storage.ConfigEnv{},
			Config: map[string]string{
				"logLevel":     "1",
				"hwaccel":      "2",
				"subInput":     "3",
				"audioEncoder": "4",
				"videoEncoder": "5",
				"id":           "id",
			},
		}
		actual := generateInputArgs(m, true)
		expected := "-loglevel 1 -hwaccel 2 -i 3 -c:a 4 -c:v 5 -preset veryfast -f hls -hls_flags" +
			" delete_segments -hls_list_size 2 -hls_allow_cache 0 /hls/id/id_sub.m3u8"
		if actual != expected {
			t.Fatalf("\nexpected:\n%v.\ngot\n%v.", expected, actual)
		}
	})
}

func TestGenRecorderArgs(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		m := Monitor{
			Env: &storage.ConfigEnv{},
			Config: map[string]string{
				"logLevel":    "1",
				"videoLength": "2",
				"id":          "id",
			},
		}
		actual, err := m.generateRecorderArgs("path")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "-y -loglevel 1 -live_start_index -1 -i /hls/id/id.m3u8 -t 120 -c:v copy path.mp4"
		if actual != expected {
			t.Fatalf("\nexpected:\n%v.\ngot\n%v.", expected, actual)
		}
	})
	t.Run("videoLengthErr", func(t *testing.T) {
		m := Monitor{
			Env: &storage.ConfigEnv{},
		}
		_, err := m.generateRecorderArgs("path")
		if err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
}

func mockVideoDuration(string) (time.Duration, error) {
	return 10 * time.Minute, nil
}

func mockVideoDurationErr(string) (time.Duration, error) {
	return 0, errors.New("mock")
}

func TestSaveRecording(t *testing.T) {
	t.Run("working", func(t *testing.T) {
		m, _, cancel := newTestMonitor(t)
		defer cancel()

		m.events = events{
			Event{
				Time: time.Time{},
			},
			Event{
				Time: time.Time{}.Add(2 * time.Minute),
				Detections: []Detection{
					Detection{
						Label: "10",
						Score: 9,
						Region: &Region{
							Rect: &ffmpeg.Rect{1, 2, 3, 4},
							Polygon: &ffmpeg.Polygon{
								ffmpeg.Point{5, 6},
								ffmpeg.Point{7, 8},
							},
						},
					},
				},
				Duration: 11,
			},
			Event{
				Time: time.Time{}.Add(11 * time.Minute),
			},
		}

		start := time.Time{}.Add(1 * time.Minute)
		tempdir := m.Env.SHMDir
		filePath := tempdir + "file"

		if err := m.saveRecording(filePath, start); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		b, err := ioutil.ReadFile(filePath + ".json")
		if err != nil {
			t.Fatalf("could not read file: %v", err)
		}
		actual := string(b)
		actual = strings.ReplaceAll(actual, " ", "")
		actual = strings.ReplaceAll(actual, "\n", "")

		expected := `{"start":"0001-01-01T00:01:00Z","end":"0001-01-01T00:11:00Z",` +
			`"events":[{"time":"0001-01-01T00:02:00Z","detections":` +
			`[{"label":"10","score":9,"region":{"rect":[1,2,3,4],` +
			`"polygon":[[5,6],[7,8]]}}],"duration":11}]}`

		if actual != expected {
			t.Fatalf("expected: %v, got: %v", expected, actual)
		}
	})
	t.Run("genThumbnailErr", func(t *testing.T) {
		m, _, cancel := newTestMonitor(t)
		defer cancel()

		m.newProcess = ffmock.NewProcessErr

		if err := m.saveRecording("", time.Time{}); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("durationErr", func(t *testing.T) {
		m, _, cancel := newTestMonitor(t)
		defer cancel()

		m.videoDuration = mockVideoDurationErr

		if err := m.saveRecording("", time.Time{}); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
	t.Run("writeFileErr", func(t *testing.T) {
		m, _, cancel := newTestMonitor(t)
		defer cancel()

		if err := m.saveRecording("/dev/null/", time.Time{}); err == nil {
			t.Fatal("expected: error, got: nil")
		}
	})
}
