package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/pkg/runregistry"
)

const (
	// registryHeartbeat обновляет только updated_at («процесс жив»): состояние рана читают из его API.
	registryHeartbeat = 30 * time.Second
	// containerPollInterval — как часто хост спрашивает /api/runtime контейнера о ране и готовности.
	containerPollInterval = time.Second
	// dockerStartAttempts — сколько раз хост пробует запустить `docker run`, пока контейнер не ответил.
	dockerStartAttempts = 3
	// dockerRunFailureCode — код docker run, когда сбой в самом docker, а не в команде контейнера.
	dockerRunFailureCode = 125
)

// dockerRetryDelay — пауза между попытками запустить контейнер.
var dockerRetryDelay = time.Second

// registryDir — каталог записей активных процессов afm в домашнем каталоге пользователя.
func registryDir() string {
	home, _ := os.UserHomeDir()
	return runregistry.Dir(home)
}

// runRegistrar публикует запись процесса afm run, чтобы host нашёл ран без ручного ввода. Это best effort:
// недоступный каталог реестра не должен мешать самому рану. Все методы безопасны на nil и после Close.
type runRegistrar struct {
	dir    string
	mu     sync.Mutex
	rec    runregistry.Record
	closed bool
	warned bool
	stop   chan struct{}
	wg     sync.WaitGroup
}

// newRunRegistrar пишет раннюю запись: она есть ещё до подготовки pipeline и worktree, поэтому падение процесса
// в долгой подготовке не остаётся незамеченным.
func newRunRegistrar(dir string, rec runregistry.Record) (*runRegistrar, error) {
	now := time.Now().UTC()
	rec.StartedAt, rec.UpdatedAt = now, now
	if err := runregistry.Publish(dir, rec); err != nil {
		return nil, err
	}
	return &runRegistrar{dir: dir, rec: rec, stop: make(chan struct{})}, nil
}

func (r *runRegistrar) InstanceID() string {
	if r == nil {
		return ""
	}
	return r.rec.InstanceID
}

func (r *runRegistrar) update(change func(rec *runregistry.Record) (changed bool)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !change(&r.rec) {
		return
	}
	r.publishLocked()
}

func (r *runRegistrar) publishLocked() {
	r.rec.UpdatedAt = time.Now().UTC()
	if err := runregistry.Publish(r.dir, r.rec); err != nil && !r.warned {
		r.warned = true
		fmt.Fprintf(os.Stderr, "afm: warning: cannot update the run registry: %v\n", err)
	}
}

// SetProject дописывает имя рана и worktree, которые известны только после разбора флоу.
func (r *runRegistrar) SetProject(runName, worktree string) {
	r.update(func(rec *runregistry.Record) bool {
		rec.Project.RunName, rec.Project.Worktree = runName, worktree
		return true
	})
}

// SetRun переключает запись на новый ран; прежний уходит в историю repeat, готовность сбрасывается.
func (r *runRegistrar) SetRun(runID string) {
	r.update(func(rec *runregistry.Record) bool {
		if rec.RunID == runID {
			return false
		}
		if rec.RunID != "" {
			rec.PreviousRunIDs = append(rec.PreviousRunIDs, rec.RunID)
		}
		rec.RunID, rec.Ready = runID, false
		return true
	})
}

func (r *runRegistrar) SetEndpoint(apiURL string) {
	r.update(func(rec *runregistry.Record) bool {
		if rec.APIURL == apiURL {
			return false
		}
		rec.APIURL = apiURL
		return true
	})
}

func (r *runRegistrar) SetReady(ready bool) {
	r.update(func(rec *runregistry.Record) bool {
		if rec.Ready == ready {
			return false
		}
		rec.Ready = ready
		return true
	})
}

// Touch обновляет только updated_at.
func (r *runRegistrar) Touch() {
	r.update(func(*runregistry.Record) bool { return true })
}

// Every вызывает fn с периодом interval до Close.
func (r *runRegistrar) Every(interval time.Duration, fn func()) {
	if r == nil {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fn()
			case <-r.stop:
				return
			}
		}
	}()
}

// Close останавливает фоновые вызовы и удаляет запись своего процесса. Повторный вызов ничего не делает.
func (r *runRegistrar) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.stop)
	r.mu.Unlock()
	// Фоновые вызовы закончились до удаления: ни один из них не вернёт запись.
	r.wg.Wait()
	_ = runregistry.Remove(r.dir, r.rec.InstanceID)
}

// loopbackURL превращает адрес сервера (localhost:порт) в endpoint, который публикуется для host.
func loopbackURL(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Port() == "" {
		return ""
	}
	return "http://" + net.JoinHostPort("127.0.0.1", u.Port())
}

// containerRuntimePoller возвращает опрос /api/runtime контейнера: хост-запись получает run_id и готовность.
// Ответ чужого процесса на этом порту (другой instance_id) игнорируется. answered становится true после первого
// ответа своего контейнера.
func containerRuntimePoller(reg *runRegistrar, apiURL string, answered *atomic.Bool) func() {
	client := &http.Client{Timeout: 2 * time.Second}
	return func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiURL+"/api/runtime", http.NoBody)
		if err != nil {
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		var rt struct {
			RunID      string `json:"run_id"`
			Ready      bool   `json:"ready"`
			InstanceID string `json:"instance_id"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rt) != nil || rt.InstanceID != reg.InstanceID() {
			return
		}
		answered.Store(true)
		if rt.RunID != "" {
			reg.SetRun(rt.RunID)
		}
		reg.SetReady(rt.Ready)
	}
}

// reexecWithRetry запускает docker run и повторяет его при сбое самого docker (код 125), пока контейнер не
// ответил: после ответа повтор поднял бы второй контейнер того же рана.
func reexecWithRetry(rc docker.ReExecConfig, started func() bool) error {
	for attempt := 1; ; attempt++ {
		err := docker.ReExec(rc)
		var exitErr *docker.SubprocessExitError
		if attempt >= dockerStartAttempts || !errors.As(err, &exitErr) || exitErr.Code != dockerRunFailureCode || started() {
			return err
		}
		time.Sleep(dockerRetryDelay)
	}
}

// openRegistrar publishes the early record of this process and starts its heartbeat. A registry that cannot be
// written costs only discoverability, so the run goes on without one.
func openRegistrar(launch launchMode, cfg config.Config, mcp mcpSettings, instanceID, launchID string) *runRegistrar {
	processStart, _ := runregistry.ProcessStart(os.Getpid())
	reg, err := newRunRegistrar(registryDir(), runregistry.Record{
		InstanceID:   instanceID,
		LaunchID:     launchID,
		PID:          os.Getpid(),
		ProcessStart: processStart,
		AFMVersion:   version,
		Mode:         launch.String(),
		Docker:       cfg.Docker.IsDockerEnabled(),
		Project:      runregistry.Project{Root: absRootDir()},
		RunsDir:      absPath(runsDir()),
		MCP:          mcp.enabled,
		Dashboard:    launch == launchDashboard,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "afm: warning: this run is not published to the run registry: %v\n", err)
		return nil
	}
	reg.Every(registryHeartbeat, reg.Touch)
	return reg
}

// watchContainer publishes the endpoint of a container that will serve on the host's loopback port and follows
// its run through /api/runtime. The returned func reports whether the container has answered.
func watchContainer(reg *runRegistrar, port int) func() bool {
	var answered atomic.Bool
	if reg != nil {
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		reg.SetEndpoint(endpoint)
		reg.Every(containerPollInterval, containerRuntimePoller(reg, endpoint, &answered))
	}
	return answered.Load
}
