package serve

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/contracts/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const runtimeTool = "afm_docker_runtime"
const runtimeImageMissing = "missing"

type runtimeRequest struct {
	MatchingImage bool   `json:"matching_image,omitempty"`
	Project       string `json:"project,omitempty" jsonschema:"registered project ID; omit for global settings"`
	Pull          bool   `json:"pull,omitempty" jsonschema:"download the configured image with user approval"`
}

type runtimeStatus struct {
	Standard  string `json:"standard,omitempty"`
	State     string `json:"state"`
	CLI       string `json:"cli,omitempty"`
	Engine    string `json:"engine,omitempty"`
	Image     string `json:"image,omitempty"`
	Required  string `json:"required,omitempty"`
	Installed string `json:"installed,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Resolve settings in the serving process, which also supplies the child's PATH and Docker context.
func (g *Gateway) dockerRuntime(ctx context.Context, in runtimeRequest) (runtimeStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return runtimeStatus{}, err
	}
	global := filepath.Join(home, ".afm")
	project := global
	if in.Project != "" {
		p, ok := g.cfg.Launcher.cfg.Config.Project(in.Project)
		if !ok {
			return runtimeStatus{}, invalidRequest("unknown project")
		}
		project = filepath.Join(p.Path, ".afm")
	}
	cfg, err := config.LoadFrom(global, project)
	if err != nil {
		return runtimeStatus{}, err
	}
	if !cfg.Docker.IsDockerEnabled() {
		return runtimeStatus{State: "native"}, nil
	}
	if cfg.Docker.Image == "" {
		cfg.Docker.Image = os.Getenv("AFM_DESKTOP_RUNTIME_IMAGE")
	}
	s := runtimeStatus{Required: strings.TrimPrefix(g.cfg.Version, "v"), Image: cfg.Docker.GetImage()}
	s.Standard = os.Getenv("AFM_DESKTOP_RUNTIME_IMAGE")
	if in.MatchingImage && s.Standard != "" {
		s.Image = s.Standard
	}
	cli, err := exec.LookPath("docker")
	if err != nil {
		s.State = "cli"
		return s, nil
	}
	s.CLI = cli
	run := func(timeout time.Duration, args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := exec.CommandContext(c, cli, args...).CombinedOutput() //nolint:gosec // cli is docker from LookPath; image references are single arguments with leading dashes rejected.
		if c.Err() != nil {
			return "", c.Err()
		}
		return strings.TrimSpace(string(out)), err
	}
	engine, err := run(4*time.Second, "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		s.State = "engine"
		s.Detail = engine
		return s, nil
	}
	s.Engine = engine
	if strings.HasPrefix(s.Image, "-") {
		return s, errors.New("invalid Docker image name")
	}
	if in.Pull {
		if out, err := run(10*time.Minute, "pull", "--quiet", s.Image); err != nil {
			s.State = runtimeImageMissing
			s.Detail = out
			if out == "" {
				s.Detail = err.Error()
			}
			return s, nil
		}
	}
	if _, err := run(4*time.Second, "image", "inspect", "--format", "{{.Id}}", s.Image); err != nil {
		s.State = runtimeImageMissing
		return s, nil
	}
	version, err := run(7*time.Second, "run", "--pull=never", "--rm", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--entrypoint", "/usr/local/bin/afm", s.Image, "--version")
	s.Installed = strings.TrimPrefix(strings.TrimPrefix(version, "afm version "), "v")
	if err != nil || s.Installed != s.Required {
		s.State = "mismatch"
		if err != nil {
			s.Detail = err.Error()
			s.Installed = ""
		}
		return s, nil
	}
	s.State = "ready"
	return s, nil
}
func (g *Gateway) registerRuntime() {
	sdkmcp.AddTool(g.server, &sdkmcp.Tool{Name: runtimeTool, Description: "Check Docker readiness for AFM. Pull only when explicitly requested. Requires runs permission."}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runtimeRequest) (*sdkmcp.CallToolResult, any, error) {
		if !g.cfg.Groups.Runs {
			return notAllowed(runtimeTool), nil, nil
		}
		s, err := g.dockerRuntime(ctx, in)
		if err != nil {
			return failure(err), nil, nil
		}
		return ok(s), nil, nil
	})
}
