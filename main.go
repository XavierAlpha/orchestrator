package main

import (
	"fmt"
	"github.com/spf13/viper"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type GlobalConfig struct {
	WorkspaceDir     string `mapstructure:"workspace_dir" yaml:"workspace_dir"`
	DefaultGoVersion string `mapstructure:"default_go_version" yaml:"default_go_version"`
}

type RepoConfig struct {
	Name      string            `mapstructure:"name"         yaml:"name"`
	GitURL    string            `mapstructure:"git_url"      yaml:"git_url"`
	Version   string            `mapstructure:"version"      yaml:"version"`
	Branch    string            `mapstructure:"branch"       yaml:"branch"`
	GoVersion string            `mapstructure:"go_version"   yaml:"go_version"`
	Platforms []string          `mapstructure:"platforms"    yaml:"platforms"`
	BuildArgs string            `mapstructure:"build_args"   yaml:"build_args"`
	Env       map[string]string `mapstructure:"env"          yaml:"env"`
}

type RootConfig struct {
	Globals GlobalConfig `yaml:"globals"`
	Repos   []RepoConfig `yaml:"repos"`
}

func runCommand(dir string, env []string, cmdName string, args ...string) error {
	cmd := exec.Command(cmdName, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func outputCommand(dir string, env []string, cmdName string, args ...string) (string, error) {
	cmd := exec.Command(cmdName, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func expandEnv(s string) string {
	return os.ExpandEnv(s)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func orchestrateOne(globals GlobalConfig, repo RepoConfig) {
	workDir := expandEnv(repo.Env["WORKSPACE"])
	if workDir == "" {
		workDir = globals.WorkspaceDir
	}

	repoDir := filepath.Join(workDir, repo.Name)
	if !exists(repoDir) {
		if err := os.MkdirAll(workDir, 0755); err != nil {
			log.Fatalf("[%s] cannot create workspace: %v", repo.Name, err)
		}
	}

	ver := expandEnv(repo.Version)
	branch := expandEnv(repo.Branch)
	if ver == "" && branch == "" {
		log.Fatalf("[%s] both version and branch are empty", repo.Name)
	}
	version := ver
	if version == "" {
		version = branch
	}

	if exists(filepath.Join(repoDir, ".git")) {
		log.Printf("[%s] git fetch & checkout %s", repo.Name, version)
		_ = runCommand(repoDir, nil, "git", "fetch", "--all", "--prune")
		if err := runCommand(repoDir, nil, "git", "checkout", version); err != nil {
			log.Fatalf("[%s] git checkout %s failed: %v", repo.Name, version, err)
		}
		_ = runCommand(repoDir, nil, "git", "pull", "--ff-only", "origin", version)
	} else {
		log.Printf("[%s] git clone %s (ref=%s)", repo.Name, repo.GitURL, version)
		if err := runCommand(workDir, nil,
			"git", "clone", "--branch", version, "--single-branch", repo.GitURL, repo.Name,
		); err != nil {
			log.Fatalf("[%s] git clone failed: %v", repo.Name, err)
		}
	}

	if version == branch {
		log.Printf("[%s] Prepare version: Fetch Tag ...", repo.Name)
		_ = runCommand(repoDir, nil, "git", "fetch", "-q", "--tags")
		if tag, err := outputCommand(repoDir, nil, "git", "describe", "--tags", "--abbrev=0"); err != nil {
			log.Printf("[%s] Latest Tag = %s", repo.Name, tag)
			version = tag
		} else {
			log.Printf("[%s] No Latest Tag： %v", repo.Name, err)
		}
	}

	shortSHA, _ := outputCommand(repoDir, nil, "git", "rev-parse", "--short=7", "HEAD")
	lastSHAFile := filepath.Join(repoDir, ".last_build_sha")
	prev, _ := os.ReadFile(lastSHAFile)
	if string(prev) == shortSHA && shortSHA != "" {
		log.Printf("[%s] no changes since %s, skip", repo.Name, shortSHA)
		return
	}
	log.Printf("[%s] new commit shortSHA %s", repo.Name, shortSHA)

	goVer := expandEnv(repo.GoVersion)
	if goVer == "" {
		goVer = globals.DefaultGoVersion
	}
	_ = runCommand("", nil, "go", "install", fmt.Sprintf("golang.org/dl/go%s@latest", goVer))
	_ = runCommand("", nil, fmt.Sprintf("go%s", goVer), "download")
	goBin := fmt.Sprintf("go%s", goVer)
	_ = runCommand(repoDir, nil, goBin, "mod", "tidy")

	artifactsDir := filepath.Join(repoDir, "artifacts")
	_ = os.MkdirAll(artifactsDir, 0755)

	for _, platform := range repo.Platforms {
		parts := strings.SplitN(platform, "/", 2)
		if len(parts) != 2 {
			log.Printf("[%s] invalid platform: %s", repo.Name, platform)
			continue
		}
		goos, goarch := parts[0], parts[1]
		bin := fmt.Sprintf("%s-%s-%s", repo.Name, goos, goarch)
		out := filepath.Join(artifactsDir, bin)

		env := os.Environ()
		env = append(env,
			"GOOS="+goos,
			"GOARCH="+goarch,
			"CGO_ENABLED=0",
			"SHORT_SHA="+shortSHA,
			"OUTPUT="+out,
			"WORKSPACE="+workDir,
		)
		for k, v := range repo.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, expandEnv(v)))
		}

		cmdStr := goBin + " " + repo.BuildArgs
		log.Printf("[%s][%s/%s] RUN: %s", repo.Name, goos, goarch, cmdStr)
		if err := runCommand(repoDir, env, "bash", "-c", cmdStr); err != nil {
			log.Fatalf("[%s][%s/%s] build failed: %v", repo.Name, goos, goarch, err)
		}
	}

	_ = os.WriteFile(lastSHAFile, []byte(shortSHA), 0644)
	log.Printf("[%s] completed, SHA=%s", repo.Name, shortSHA)
}

func main() {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")

	if err := v.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	var cfg RootConfig
	if err := v.Unmarshal(&cfg); err != nil {
		log.Fatalf("Error unmarshalling config: %v", err)
	}

	dg := expandEnv(cfg.Globals.DefaultGoVersion)
	if dg == "" {
		dg = "1.24"
	}
	cfg.Globals.DefaultGoVersion = dg
	log.Printf("Using default_go_version = %q", dg)

	for i, r := range cfg.Repos {
		r.Version = expandEnv(r.Version)
		r.Branch = expandEnv(r.Branch)
		r.GoVersion = expandEnv(r.GoVersion)
		for k, v := range r.Env {
			r.Env[k] = expandEnv(v)
		}

		// fallback version -> branch
		if strings.TrimSpace(r.Version) == "" {
			r.Version = r.Branch
		}
		if r.GitURL == "" {
			log.Fatalf("repo[%d] name=%s: git_url is required", i, r.Name)
		}
		if r.Version == "" {
			log.Fatalf("repo[%d] name=%s: both version and branch are empty", i, r.Name)
		}
		if len(r.Platforms) == 0 {
			log.Fatalf("repo[%d] name=%s: platforms must be defined", i, r.Name)
		}

		cfg.Repos[i] = r
	}

	for _, repo := range cfg.Repos {
		log.Printf(">>> Building %s @ %s", repo.Name, repo.Version)
		orchestrateOne(cfg.Globals, repo)
	}
}
