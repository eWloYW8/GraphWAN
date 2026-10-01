//go:build linux || darwin || windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/model"
	updater "github.com/eWloYW8/GraphWAN/internal/update"
	"github.com/kardianos/service"
)

type managedAgentKey struct{}

func agentUpdater(ctx context.Context, data string) (agent.Updater, error) {
	name, _ := ctx.Value(managedAgentKey{}).(string)
	if name == "" {
		return nil, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	data, err = filepath.Abs(data)
	if err != nil {
		return nil, err
	}
	receipt := updater.Receipt{Name: name, Executable: executable, Data: data}
	// Older built-in service installations acquire the receipt on their first run
	// of an updater-capable binary. Foreground/manual services never acquire it.
	if err = updater.WriteJSON(filepath.Join(data, "service.json"), receipt); err != nil {
		return nil, err
	}
	return updater.NewManager(ctx, receipt, version, launchUpdateHelper), nil
}
func runAgentUpdate(args []string) error {
	fs := flag.NewFlagSet("agent update", flag.ContinueOnError)
	data := fs.String("data-dir", "./graphwan-agent-data", "registered service data directory")
	source := fs.String("source", "github", "download source: github or server")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *source != "github" && *source != "server" {
		return errors.New("--source must be github or server")
	}
	absolute, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(absolute, "service.json"))
	if err != nil {
		return errors.New("agent must be running under graphwan agent service with this data directory")
	}
	var r updater.Receipt
	if err = json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Data != absolute || !serviceNamePattern.MatchString(r.Name) {
		return errors.New("invalid service receipt")
	}
	s, err := service.New(&managedProgram{}, &service.Config{Name: r.Name})
	if err != nil {
		return err
	}
	state, err := s.Status()
	if err != nil {
		return err
	}
	if state != service.StatusRunning {
		return errors.New("start the registered service before requesting an update")
	}
	j := updater.ReadJournal(absolute)
	if j.Status.Phase == "downloading" || j.Status.Phase == "installing" {
		return errors.New("update already in progress")
	}
	request := model.UpdateRequest{ID: model.NewID(), Source: *source, CreatedAt: time.Now().UTC()}
	path := filepath.Join(absolute, "update-request.json")
	// A CLI request is picked up on the next connected control tick. Never open
	// the Agent's locked database or put enrollment credentials in a subprocess.
	raw, err = json.Marshal(request)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err == nil {
		err = e
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	fmt.Printf("Update queued for %s (%s). Status: %s\n", r.Name, *source, filepath.Join(absolute, "update-state.json"))
	return nil
}

type updateJob struct {
	Receipt   updater.Receipt
	Request   model.UpdateRequest
	Stage     string
	Helper    string
	Directory string
}

func copyExecutable(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	e := out.Close()
	if err == nil {
		err = e
	}
	return err
}
func helperConfig(job updateJob) *service.Config {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return &service.Config{Name: job.Helper, DisplayName: "GraphWAN update", Executable: filepath.Join(job.Directory, "helper"+suffix), Arguments: []string{"_apply-update", filepath.Join(job.Directory, "job.json")}, WorkingDirectory: job.Directory, Option: service.KeyValue{"RunAtLoad": true, "KeepAlive": false, "OnFailure": "noaction", "SystemdScript": updateSystemdUnit}}
}
func launchUpdateHelper(r updater.Receipt, stage string, req model.UpdateRequest) error {
	lock := r.Executable + ".update-lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("another update owns %s: %w", lock, err)
	}
	f.Close()
	launched := false
	defer func() {
		if !launched {
			os.Remove(lock)
		}
	}()
	// Retire completed helper copies (Windows locks the running helper image).
	entries, _ := os.ReadDir(r.Data)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "updater-") {
			oldDir := filepath.Join(r.Data, entry.Name())
			raw, e := os.ReadFile(filepath.Join(oldDir, "job.json"))
			var old updateJob
			if e == nil && json.Unmarshal(raw, &old) == nil && old.Receipt.Executable == r.Executable && old.Request.ID != req.ID {
				_ = os.RemoveAll(oldDir)
			}
		}
	}
	dir, err := os.MkdirTemp(r.Data, "updater-")
	if err != nil {
		return err
	}
	job := updateJob{Receipt: r, Request: req, Stage: stage, Helper: "graphwan-update-" + string(req.ID), Directory: dir}
	config := helperConfig(job)
	if err = copyExecutable(r.Executable, config.Executable); err != nil {
		return err
	}
	if err = updater.WriteJSON(filepath.Join(dir, "job.json"), job); err != nil {
		return err
	}
	helper, err := service.New(&updateProgram{job: job}, config)
	if err != nil {
		return err
	}
	if err = helper.Install(); err != nil {
		return err
	}
	if err = helper.Start(); err != nil {
		_ = helper.Uninstall()
		return err
	}
	launched = true
	return nil
}

type updateProgram struct{ job updateJob }

func (p *updateProgram) Start(s service.Service) error {
	go func() {
		err := applyUpdate(p.job)
		j := updater.ReadJournal(p.job.Receipt.Data)
		if j.Status.RequestID == p.job.Request.ID {
			j.Status.Phase = "succeeded"
			j.Status.Error = ""
			if err != nil {
				j.Status.Phase = "failed"
				j.Status.Error = err.Error()
			}
			j.Status.UpdatedAt = time.Now().UTC()
			_ = updater.SaveJournal(p.job.Receipt.Data, j)
		}
		_ = os.Remove(p.job.Stage)
		_ = os.Remove(p.job.Receipt.Executable + ".update-lock")
		// Windows cannot delete this helper while it is executing. The next update
		// removes old helper directories after their job is no longer active.
		if runtime.GOOS != "windows" {
			_ = os.RemoveAll(p.job.Directory)
		}
		if runtime.GOOS == "darwin" {
			_ = os.Remove(filepath.Join("/Library/LaunchDaemons", p.job.Helper+".plist"))
			_ = exec.Command("launchctl", "remove", p.job.Helper).Run()
		} else {
			_ = s.Uninstall()
		}
		os.Exit(0)
	}()
	return nil
}
func (*updateProgram) Stop(service.Service) error { return nil }
func runUpdateHelper(args []string) error {
	if len(args) != 1 {
		return errors.New("missing update job")
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var job updateJob
	if err = json.Unmarshal(raw, &job); err != nil {
		return err
	}
	if err = job.Request.Validate(); err != nil {
		return err
	}
	if !serviceNamePattern.MatchString(job.Receipt.Name) || job.Helper != "graphwan-update-"+string(job.Request.ID) || job.Directory != filepath.Dir(args[0]) || filepath.Dir(job.Stage) != filepath.Dir(job.Receipt.Executable) {
		return errors.New("invalid update job")
	}
	j := updater.ReadJournal(job.Receipt.Data)
	if j.Status.RequestID != job.Request.ID || j.Status.Phase != "installing" {
		return errors.New("update job is no longer active")
	}
	s, err := service.New(&updateProgram{job: job}, helperConfig(job))
	if err != nil {
		return err
	}
	return s.Run()
}
func binaryVersion(path string, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "GraphWAN "+want {
		return errors.New("downloaded binary reports an unexpected version")
	}
	return nil
}
func waitService(s service.Service, want service.Status, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := s.Status()
		if err == nil && status == want {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("service did not reach expected state")
}
func applyUpdate(job updateJob) error {
	r := job.Receipt
	if err := updater.Verify(job.Stage, job.Request.Asset); err != nil {
		return err
	}
	// Windows requires an .exe extension when probing a staged executable.
	stage := job.Stage
	if runtime.GOOS == "windows" {
		stage += ".exe"
		if err := os.Rename(job.Stage, stage); err != nil {
			return err
		}
		defer os.Remove(stage)
	}
	if err := binaryVersion(stage, job.Request.Asset.Version); err != nil {
		return fmt.Errorf("validate executable: %w", err)
	}
	info, err := os.Stat(r.Executable)
	if err != nil {
		return err
	}
	if err = os.Chmod(stage, info.Mode().Perm()); err != nil {
		return err
	}
	s, err := service.New(&managedProgram{}, &service.Config{Name: r.Name})
	if err != nil {
		return err
	}
	if err = s.Stop(); err != nil {
		return err
	}
	if err = waitService(s, service.StatusStopped, 40*time.Second); err != nil {
		_ = s.Start()
		return err
	}
	backup := r.Executable + ".previous"
	if err = os.Remove(backup); err != nil && !os.IsNotExist(err) {
		_ = s.Start()
		return err
	}
	// A stopped Windows service may retain its image handle briefly.
	deadline := time.Now().Add(15 * time.Second)
	for {
		err = os.Rename(r.Executable, backup)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		_ = s.Start()
		return err
	}
	restore := func(cause error) error {
		_ = s.Stop()
		_ = waitService(s, service.StatusStopped, 30*time.Second)
		_ = os.Remove(r.Executable)
		if err := os.Rename(backup, r.Executable); err != nil {
			return fmt.Errorf("%v; rollback failed: %w", cause, err)
		}
		if err := s.Start(); err != nil {
			return fmt.Errorf("%v; old binary restored but restart failed: %w", cause, err)
		}
		return fmt.Errorf("%v; previous binary restored", cause)
	}
	if err = os.Rename(stage, r.Executable); err != nil {
		return restore(err)
	}
	if err = s.Start(); err != nil {
		return restore(err)
	}
	if err = waitService(s, service.StatusRunning, 30*time.Second); err != nil {
		return restore(err)
	}
	for i := 0; i < 10; i++ {
		time.Sleep(time.Second)
		state, e := s.Status()
		if e != nil || state != service.StatusRunning {
			return restore(errors.New("updated service exited during startup"))
		}
	}
	return nil
}

const updateSystemdUnit = `[Unit]
Description=GraphWAN update
[Service]
Type=simple
ExecStart={{.Path|cmd}}{{range .Arguments}} {{.|cmd}}{{end}}
WorkingDirectory={{.WorkingDirectory}}
Restart=no
TimeoutStopSec=120
UMask=0077
[Install]
WantedBy=multi-user.target
`
